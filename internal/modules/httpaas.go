package modules

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"np2/appgo/internal/config"
	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/validate"
)

const (
	// Limits applied before any content is sent to the guest. They prevent a
	// malicious zip from exhausting the instance disk or the request pipeline.
	httpaaSMaxZipEntries   = 5000
	httpaaSMaxUncompressed = 512 << 20 // 512 MiB
	httpaaSStateVersion    = 1
	httpaaSStatusRunning   = "running"
	httpaaSStatusStopped   = "stopped"
	httpaaSStatusMissing   = "missing"
	httpaaSStatusUnknown   = "unknown"
	httpaaSGuestPoll       = 2 * time.Second
	dnsProbeTimeout        = 5 * time.Second
)

var (
	reVBoxSATA    = regexp.MustCompile(`^"?SATA-[0-9]+-[0-9]+"?="(.+)"$`)
	reMachineProp = regexp.MustCompile(`^"?([^"=]+)"?="?(.*?)"?$`)
)

// HTTPaaSProvisionRequest is a user request to create a new Apache instance.
// Zip is optional: when empty the instance is created with a placeholder page
// and the content is published later with Publish ("Aceptar" / "Publicar").
type HTTPaaSProvisionRequest struct {
	Hostname string
	ZipName  string
	Zip      []byte
}

// HTTPaaSInstance is one provisioned Apache server, as shown on the dashboard
// and persisted in the inventory.
type HTTPaaSInstance struct {
	Hostname     string    `json:"hostname"`
	FQDN         string    `json:"fqdn"`
	IP           string    `json:"ip"`
	Prefix       int       `json:"prefix,omitempty"`
	VM           string    `json:"vm"`
	Disk         string    `json:"disk,omitempty"`
	ParentDisk   string    `json:"parent_disk,omitempty"`
	DocumentRoot string    `json:"document_root"`
	URL          string    `json:"url"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	ContentName  string    `json:"content,omitempty"`
	PublishedAt  time.Time `json:"published_at,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
}

// Address returns the IP in CIDR notation (192.168.10.30/24) when the prefix is
// known, as shown on the dashboard.
func (i HTTPaaSInstance) Address() string {
	if i.Prefix > 0 {
		return fmt.Sprintf("%s/%d", i.IP, i.Prefix)
	}
	return i.IP
}

// Published reports whether user content has been deployed to the instance.
func (i HTTPaaSInstance) Published() bool { return i.ContentName != "" }

// IPURL is the direct address alternative when the user's resolver does not use
// the lab DNS yet.
func (i HTTPaaSInstance) IPURL() string {
	return fmt.Sprintf("http://%s/", i.IP)
}

// HTTPaaSManager orchestrates VirtualBox, Bind9 and Apache for the managed
// hosting module. Mutating operations are serialised because they allocate
// addresses and update a shared inventory.
type HTTPaaSManager struct {
	opts      config.HTTPaaSConfig
	host      runner.Runner
	mu        sync.Mutex
	instances map[string]HTTPaaSInstance
	// baseDisk is the multiattach root used as parent; templateChild is the
	// automatic differencing disk the template itself runs on. Both are
	// protected from deletion.
	baseDisk      string
	templateChild string
}

type httpaaSStateFile struct {
	Version   int               `json:"version"`
	Instances []HTTPaaSInstance `json:"instances"`
}

// NewHTTPaaSManager builds the manager and loads the persisted inventory.
func NewHTTPaaSManager(opts config.HTTPaaSConfig) (*HTTPaaSManager, error) {
	if strings.TrimSpace(opts.VBoxManage) == "" {
		opts.VBoxManage = "VBoxManage"
	}
	if strings.TrimSpace(opts.TemplateVM) == "" {
		opts.TemplateVM = "template_server"
	}
	if strings.TrimSpace(opts.ApacheRoot) == "" {
		opts.ApacheRoot = "/var/www"
	}
	if opts.HTTPPort == 0 {
		opts.HTTPPort = 80
	}
	if opts.BootTimeout <= 0 {
		opts.BootTimeout = 4 * time.Minute
	}
	if opts.DNSTTL <= 0 {
		opts.DNSTTL = 300
	}
	if strings.TrimSpace(opts.StatePath) == "" {
		opts.StatePath = "./httpaas/instances.json"
	}
	if strings.TrimSpace(opts.DiskDir) == "" {
		opts.DiskDir = "./httpaas/disks"
	}
	statePath, err := filepath.Abs(opts.StatePath)
	if err != nil {
		return nil, fmt.Errorf("ruta del inventario HTTPaaS: %w", err)
	}
	diskDir, err := filepath.Abs(opts.DiskDir)
	if err != nil {
		return nil, fmt.Errorf("ruta de discos HTTPaaS: %w", err)
	}
	opts.StatePath = statePath
	opts.DiskDir = diskDir

	m := &HTTPaaSManager{
		opts:      opts,
		host:      runner.NewLocal(opts.HostSudo),
		instances: make(map[string]HTTPaaSInstance),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Enabled reports whether the module is configured.
func (m *HTTPaaSManager) Enabled() bool { return m != nil && m.opts.Enabled }

// Domain returns the DNS suffix used for the links.
func (m *HTTPaaSManager) Domain() string {
	if m == nil {
		return ""
	}
	return m.opts.Domain
}

// IPRange returns a human-readable range for the form.
func (m *HTTPaaSManager) IPRange() string {
	if m == nil {
		return ""
	}
	return m.opts.IPStart + " – " + m.opts.IPEnd
}

// Template returns the template VM name.
func (m *HTTPaaSManager) Template() string {
	if m == nil {
		return ""
	}
	return m.opts.TemplateVM
}

func (m *HTTPaaSManager) load() error {
	data, err := os.ReadFile(m.opts.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("leer inventario HTTPaaS %s: %w", m.opts.StatePath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var state httpaaSStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("inventario HTTPaaS corrupto (%s): %w", m.opts.StatePath, err)
	}
	for _, inst := range state.Instances {
		host, err := validate.Hostname(inst.Hostname)
		if err != nil {
			return fmt.Errorf("inventario HTTPaaS: entrada inválida: %w", err)
		}
		if _, err := validate.IPv4(inst.IP); err != nil {
			return fmt.Errorf("inventario HTTPaaS: IP inválida para %s: %w", host, err)
		}
		if strings.TrimSpace(inst.VM) == "" {
			return fmt.Errorf("inventario HTTPaaS: la entrada %s no tiene máquina virtual", host)
		}
		// Remember the template disk recorded at provision time so deletion can
		// protect it even before VirtualBox is queried.
		if m.baseDisk == "" && strings.TrimSpace(inst.ParentDisk) != "" {
			m.baseDisk = inst.ParentDisk
		}
		m.instances[host] = inst
	}
	return nil
}

func (m *HTTPaaSManager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.opts.StatePath), 0700); err != nil {
		return fmt.Errorf("crear directorio del inventario: %w", err)
	}
	list := make([]HTTPaaSInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		list = append(list, inst)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].Hostname < list[j].Hostname
		}
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	data, err := json.MarshalIndent(httpaaSStateFile{Version: httpaaSStateVersion, Instances: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("serializar inventario HTTPaaS: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.opts.StatePath), ".instances-*.json")
	if err != nil {
		return fmt.Errorf("crear inventario temporal: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("permisos del inventario: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("escribir inventario temporal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cerrar inventario temporal: %w", err)
	}
	if err := os.Rename(tmpName, m.opts.StatePath); err != nil {
		return fmt.Errorf("publicar inventario HTTPaaS: %w", err)
	}
	return nil
}

// List returns a sorted copy of the inventory.
func (m *HTTPaaSManager) List() []HTTPaaSInstance {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]HTTPaaSInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Refresh updates the stored power state of each instance. Errors are returned
// only when VirtualBox itself cannot be reached; a missing VM is recorded as
// "missing" so the operator can delete the inventory entry.
func (m *HTTPaaSManager) Refresh(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for name, inst := range m.instances {
		res := m.host.Run(ctx, nil, m.opts.VBoxManage, "showvminfo", inst.VM, "--machinereadable")
		if res.TransportFailed() {
			return fmt.Errorf("no se pudo consultar VirtualBox: %s", res.ErrorString())
		}
		status := httpaaSStatusUnknown
		if res.Ok() {
			status = statusFromVBoxState(parseMachineReadable(res.Stdout)["VMState"])
		} else if vmNotFound(res) {
			status = httpaaSStatusMissing
		}
		if status != inst.Status {
			inst.Status = status
			m.instances[name] = inst
			changed = true
		}
	}
	if changed {
		return m.saveLocked()
	}
	return nil
}

// Provision creates the VM, configures it, registers DNS and deploys the zip.
func (m *HTTPaaSManager) Provision(ctx context.Context, req HTTPaaSProvisionRequest) *op.Report {
	rep := op.New("Aprovisionar instancia HTTPaaS")
	defer rep.Finish()
	if m == nil || !m.opts.Enabled {
		return rep.Fail(errors.New("el módulo HTTPaaS no está habilitado"))
	}

	host, err := m.normalizeHostname(req.Hostname)
	if err != nil {
		return rep.Fail(err)
	}
	rep.Action = "Aprovisionar " + host + "." + m.opts.Domain

	zipData := req.Zip
	if len(zipData) > 0 {
		flat, err := m.checkZip(rep, req.ZipName, zipData)
		if err != nil {
			return rep.Fail(err)
		}
		zipData = flat
	} else {
		rep.Note("Sin contenido adjunto: la instancia mostrará una página provisional hasta que use «Publicar».")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.instances[host]; exists {
		return rep.Failf("ya existe una instancia para %q", host)
	}
	vmName := m.vmName(host)
	if sameVM(vmName, m.opts.TemplateVM) || sameVM(vmName, "dns_server") {
		return rep.Failf("el nombre %q coincide con una máquina protegida de la infraestructura", vmName)
	}
	if res := m.host.Run(ctx, nil, m.opts.VBoxManage, "showvminfo", vmName, "--machinereadable"); res.Ok() {
		return rep.Failf("ya existe una máquina virtual llamada %q; elimínela o elija otro nombre de host", vmName)
	}

	ip, err := nextFreeIP(m.opts.IPStart, m.opts.IPEnd, m.usedIPsLocked(), m.opts.Gateway, m.opts.DNSServer)
	if err != nil {
		return rep.Fail(err)
	}
	fqdn := host + "." + m.opts.Domain
	inst := HTTPaaSInstance{
		Hostname:     host,
		FQDN:         fqdn,
		IP:           ip,
		Prefix:       m.opts.Netmask,
		VM:           vmName,
		DocumentRoot: path.Join(m.opts.ApacheRoot, host),
		URL:          m.siteURL(fqdn),
		Status:       httpaaSStatusUnknown,
		CreatedAt:    time.Now().UTC(),
	}
	rep.Note("Dirección asignada: %s (%s).", ip, fqdn)

	baseDisk, err := m.checkPrerequisites(ctx, rep)
	if err != nil {
		return rep.Fail(err)
	}
	inst.ParentDisk = baseDisk
	inst.Disk = filepath.Join(m.opts.DiskDir, host+".vdi")

	var (
		success   bool
		vmCreated bool
		dnsAdded  bool
	)
	defer func() {
		if success {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		m.rollback(cleanupCtx, rep, vmCreated, dnsAdded, inst)
	}()

	if err := os.MkdirAll(m.opts.DiskDir, 0700); err != nil {
		return rep.Failf("crear directorio de discos: %v", err)
	}
	if err := m.createDisk(ctx, rep, inst); err != nil {
		return rep.Fail(err)
	}
	created, err := m.createVM(ctx, rep, inst)
	vmCreated = created
	if err != nil {
		return rep.Fail(err)
	}

	if res, ok := m.vboxRequire(ctx, rep, "Encender "+vmName, "startvm", vmName, "--type", "headless"); !ok {
		_ = res
		return rep
	}

	gc, err := m.waitGuest(ctx, rep, vmName)
	if err != nil {
		return rep.Fail(err)
	}

	if err := m.configureGuest(ctx, rep, gc, inst); err != nil {
		return rep.Fail(err)
	}
	if len(zipData) > 0 {
		if err := m.deployContent(ctx, rep, gc, inst, zipData); err != nil {
			return rep.Fail(err)
		}
		inst.ContentName = req.ZipName
		inst.PublishedAt = time.Now().UTC()
	} else if err := m.deployPlaceholder(ctx, rep, gc, inst); err != nil {
		return rep.Fail(err)
	}
	if err := m.registerDNS(ctx, rep, inst.FQDN, inst.IP); err != nil {
		return rep.Fail(err)
	}
	dnsAdded = true

	inst.Status = httpaaSStatusRunning
	m.instances[host] = inst
	if err := m.saveLocked(); err != nil {
		delete(m.instances, host)
		return rep.Fail(err)
	}
	success = true

	rep.Note("Instancia disponible en %s", inst.URL)
	rep.Note("Acceso alternativo por IP: http://%s/", inst.IP)
	rep.Note("Máquina virtual: %s · disco diferencial: %s", inst.VM, inst.Disk)
	rep.Summary = fmt.Sprintf("%s.%s quedó aprovisionado en %s.", host, m.opts.Domain, inst.Address())
	if !inst.Published() {
		rep.Summary += " Seleccione el .zip y pulse «Publicar» para desplegar el contenido."
	}
	return rep
}

// HTTPaaSPublishRequest deploys (or redeploys) a .zip on an existing instance.
type HTTPaaSPublishRequest struct {
	Hostname string
	ZipName  string
	Zip      []byte
}

// Publish sends the zip to an instance created earlier and extracts it in its
// document root, replacing any previous content. A stopped VM is powered on.
func (m *HTTPaaSManager) Publish(ctx context.Context, req HTTPaaSPublishRequest) *op.Report {
	rep := op.New("Publicar contenido HTTPaaS")
	defer rep.Finish()
	if m == nil || !m.opts.Enabled {
		return rep.Fail(errors.New("el módulo HTTPaaS no está habilitado"))
	}
	host, err := m.normalizeHostname(req.Hostname)
	if err != nil {
		return rep.Fail(err)
	}
	rep.Action = "Publicar " + req.ZipName + " en " + host + "." + m.opts.Domain
	if len(req.Zip) == 0 {
		return rep.Fail(errors.New("seleccione el archivo .zip con el contenido web antes de publicar"))
	}
	zipData, err := m.checkZip(rep, req.ZipName, req.Zip)
	if err != nil {
		return rep.Fail(err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.instances[host]
	if !ok {
		return rep.Failf("no existe una instancia %q; escriba el nombre y pulse «Aceptar» para crearla primero", host)
	}
	if !m.ownedVM(inst.VM, host) {
		return rep.Failf("la entrada %q apunta a la máquina %q, que no fue creada por HTTPaaS", host, inst.VM)
	}

	stateRes := m.host.Run(ctx, nil, m.opts.VBoxManage, "showvminfo", inst.VM, "--machinereadable")
	rep.Step("Consultar estado de "+inst.VM, stateRes)
	if !stateRes.Ok() {
		if vmNotFound(stateRes) {
			return rep.Failf("la máquina %s ya no existe en VirtualBox; elimine la instancia y vuelva a crearla", inst.VM)
		}
		return rep.Failf("no se pudo consultar la máquina %s: %s", inst.VM, stateRes.ErrorString())
	}
	if state := statusFromVBoxState(parseMachineReadable(stateRes.Stdout)["VMState"]); state != httpaaSStatusRunning {
		if _, ok := m.vboxRequire(ctx, rep, "Encender "+inst.VM, "startvm", inst.VM, "--type", "headless"); !ok {
			return rep
		}
	}
	gc, err := m.waitGuest(ctx, rep, inst.VM)
	if err != nil {
		return rep.Fail(err)
	}
	if err := m.deployContent(ctx, rep, gc, inst, zipData); err != nil {
		return rep.Fail(err)
	}

	inst.ContentName = req.ZipName
	inst.PublishedAt = time.Now().UTC()
	inst.Status = httpaaSStatusRunning
	inst.LastError = ""
	m.instances[host] = inst
	if err := m.saveLocked(); err != nil {
		return rep.Fail(err)
	}
	rep.Note("Contenido disponible en %s", inst.URL)
	rep.Summary = fmt.Sprintf("%s se publicó en %s (%s).", req.ZipName, inst.FQDN, inst.Address())
	return rep
}

// checkZip enforces the upload limits and inspects the archive before any byte
// reaches the guest.
func (m *HTTPaaSManager) checkZip(rep *op.Report, name string, data []byte) ([]byte, error) {
	if max := int64(m.opts.MaxUploadMiB) << 20; max > 0 && int64(len(data)) > max {
		return nil, fmt.Errorf("el archivo supera el máximo configurado (%d MiB)", m.opts.MaxUploadMiB)
	}
	if err := validateZipPayload(data, maxUncompressedFor(m.opts.MaxUploadMiB), httpaaSMaxZipEntries); err != nil {
		return nil, err
	}
	rep.Note("Archivo recibido: %s (%d bytes). Se validaron las rutas y los enlaces simbólicos del zip.", name, len(data))

	flat, root, hidden, err := flattenZipRoot(data)
	if err != nil {
		return nil, err
	}
	if root != "" {
		rep.Note("El zip traía todo dentro de la carpeta %q; se publicó su contenido en la raíz del sitio.", root)
	}
	if len(hidden) > 0 {
		rep.Warn("El zip incluye archivos ocultos o de control de versiones (%s). Apache los bloquea, pero es mejor no subirlos.",
			strings.Join(hidden, ", "))
	}
	return flat, nil
}

// Delete powers off and removes an instance, its DNS record and its inventory
// entry. The template and the DNS server are explicitly protected.
func (m *HTTPaaSManager) Delete(ctx context.Context, rawHostname string) *op.Report {
	rep := op.New("Eliminar instancia HTTPaaS")
	defer rep.Finish()
	if m == nil || !m.opts.Enabled {
		return rep.Fail(errors.New("el módulo HTTPaaS no está habilitado"))
	}
	host, err := m.normalizeHostname(rawHostname)
	if err != nil {
		return rep.Fail(err)
	}
	rep.Action = "Eliminar " + host + "." + m.opts.Domain

	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.instances[host]
	if !ok {
		return rep.Failf("no existe una instancia registrada para %q", host)
	}
	// Only machines created by this module may be removed. This prevents a
	// tampered or stale inventory entry from deleting an unrelated VM.
	if !m.ownedVM(inst.VM, host) {
		return rep.Failf("la entrada %q apunta a la máquina %q, que no fue creada por HTTPaaS", host, inst.VM)
	}
	if sameVM(inst.VM, m.opts.TemplateVM) || sameVM(inst.VM, "dns_server") {
		return rep.Failf("la máquina %q está protegida y no puede eliminarse desde HTTPaaS", inst.VM)
	}
	if m.baseDisk == "" && strings.TrimSpace(inst.ParentDisk) != "" {
		m.baseDisk = inst.ParentDisk
	}
	if strings.TrimSpace(inst.ParentDisk) != "" && samePath(inst.Disk, inst.ParentDisk) {
		return rep.Failf("el disco %s pertenece a la plantilla y está protegido", inst.Disk)
	}
	if err := m.checkNotProtectedDisk(inst.Disk); err != nil {
		return rep.Fail(err)
	}

	// Power off before removing the medium. A missing VM is tolerated: the
	// inventory may be stale after a manual deletion.
	stateRes := m.host.Run(ctx, nil, m.opts.VBoxManage, "showvminfo", inst.VM, "--machinereadable")
	rep.OptionalStep("Consultar estado de "+inst.VM, stateRes)
	vmExists := stateRes.Ok()
	if vmExists {
		state := parseMachineReadable(stateRes.Stdout)["VMState"]
		switch strings.ToLower(strings.TrimSpace(state)) {
		case "poweroff", "aborted", "saved":
			// Already stopped; unregistervm --delete removes any saved state.
		default:
			if _, ok := m.vboxRequire(ctx, rep, "Apagar "+inst.VM, "controlvm", inst.VM, "poweroff"); !ok {
				return rep
			}
		}
	} else if stateRes.TransportFailed() {
		return rep.Failf("no se pudo consultar VirtualBox: %s", stateRes.ErrorString())
	} else if !vmNotFound(stateRes) {
		return rep.Failf("no se pudo consultar la máquina %s: %s", inst.VM, stateRes.ErrorString())
	} else {
		rep.Note("La máquina %s ya no existe en VirtualBox; se limpiará el inventario y el DNS.", inst.VM)
	}

	if err := m.removeDNS(ctx, rep, inst.FQDN); err != nil {
		return rep.Fail(err)
	}

	if vmExists {
		if _, ok := m.vboxRequire(ctx, rep, "Eliminar máquina virtual "+inst.VM, "unregistervm", inst.VM, "--delete"); !ok {
			return rep
		}
	}
	if err := m.removeDisk(ctx, inst.Disk); err != nil {
		rep.Warn("No se pudo borrar el disco diferencial %s: %v", inst.Disk, err)
	}

	delete(m.instances, host)
	if err := m.saveLocked(); err != nil {
		return rep.Fail(err)
	}
	rep.Summary = fmt.Sprintf("La instancia %s.%s y su registro DNS se eliminaron.", host, m.opts.Domain)
	return rep
}

func (m *HTTPaaSManager) normalizeHostname(raw string) (string, error) {
	host, err := validate.Hostname(raw)
	if err != nil {
		return "", err
	}
	switch host {
	case "ns", "dns-server", "dns_server", "template", "template-server", "template_server", "localhost":
		return "", fmt.Errorf("el nombre %q está reservado por la infraestructura", host)
	}
	if _, err := validate.Domain("el dominio", m.opts.Domain); err != nil {
		return "", err
	}
	if _, err := validate.IPv4(m.opts.IPStart); err != nil {
		return "", err
	}
	if _, err := validate.IPv4(m.opts.IPEnd); err != nil {
		return "", err
	}
	return host, nil
}

func (m *HTTPaaSManager) vmName(host string) string {
	return m.opts.VMPrefix + host
}

// ownedVM reports whether vm is the machine this module creates for host. The
// legacy "httpaas-" prefix is accepted so older inventories can be cleaned up.
func (m *HTTPaaSManager) ownedVM(vm, host string) bool {
	if sameVM(vm, m.opts.TemplateVM) || sameVM(vm, "dns_server") {
		return false
	}
	return sameVM(vm, m.vmName(host)) || sameVM(vm, "httpaas-"+host)
}

func (m *HTTPaaSManager) siteURL(fqdn string) string {
	if m.opts.HTTPPort == 80 {
		return "http://" + fqdn + "/"
	}
	return fmt.Sprintf("http://%s:%d/", fqdn, m.opts.HTTPPort)
}

func (m *HTTPaaSManager) usedIPsLocked() map[string]bool {
	used := make(map[string]bool, len(m.instances))
	for _, inst := range m.instances {
		used[inst.IP] = true
	}
	return used
}

// checkPrerequisites validates the template, its disk and the DNS tooling
// before the first irreversible operation.
func (m *HTTPaaSManager) checkPrerequisites(ctx context.Context, rep *op.Report) (string, error) {
	if res := m.host.Run(ctx, nil, m.opts.VBoxManage, "--version"); !res.Ok() {
		rep.Step("Comprobar VBoxManage", res)
		return "", fmt.Errorf("VBoxManage no está disponible: %s", res.ErrorString())
	} else {
		rep.Step("Comprobar VBoxManage", res)
	}

	if m.opts.NetworkMode == "hostonly" {
		res := m.host.Run(ctx, nil, m.opts.VBoxManage, "list", "hostonlyifs")
		rep.Step("Comprobar red host-only "+m.opts.NetworkName, res)
		if !res.Ok() {
			return "", fmt.Errorf("no se pudieron listar las redes host-only: %s", res.ErrorString())
		}
		if !listContainsInterface(res.Stdout, m.opts.NetworkName) {
			return "", fmt.Errorf("la red host-only %q no existe; créela con scripts/01-host-network.sh", m.opts.NetworkName)
		}
	} else {
		res := m.host.Run(ctx, nil, m.opts.VBoxManage, "list", "bridgedifs")
		rep.Step("Comprobar adaptador puente "+m.opts.NetworkName, res)
		if !res.Ok() {
			return "", fmt.Errorf("no se pudieron listar los adaptadores puente: %s", res.ErrorString())
		}
		if !listContainsInterface(res.Stdout, m.opts.NetworkName) {
			return "", fmt.Errorf("el adaptador puente %q no existe en el host", m.opts.NetworkName)
		}
	}

	res := m.host.Run(ctx, nil, m.opts.VBoxManage, "showvminfo", m.opts.TemplateVM, "--machinereadable")
	rep.Step("Inspeccionar plantilla "+m.opts.TemplateVM, res)
	if !res.Ok() {
		return "", fmt.Errorf("no se encontró la plantilla %q: %s", m.opts.TemplateVM, res.ErrorString())
	}
	props := parseMachineReadable(res.Stdout)
	state := props["VMState"]
	if !strings.EqualFold(state, "poweroff") {
		return "", fmt.Errorf("la plantilla %q debe estar apagada para crear discos diferenciales (estado: %s)", m.opts.TemplateVM, state)
	}

	attached := discoverTemplateDisk(res.Stdout)
	start := strings.TrimSpace(m.opts.TemplateDisk)
	if start == "" {
		start = attached
	}
	if start == "" {
		return "", fmt.Errorf("no se encontró el disco de la plantilla %q; configure --httpaas-template-disk", m.opts.TemplateVM)
	}
	// With a multiattach disk the template itself runs on an automatic
	// differencing child (Snapshots/{uuid}.vdi). The instances must hang from
	// the multiattach root, not from that writable child.
	baseDisk, err := m.resolveBaseDisk(ctx, rep, start)
	if err != nil {
		return "", err
	}
	if attached != "" && !samePath(attached, baseDisk) {
		m.templateChild = attached
		rep.Warn("La plantilla trabaja sobre su disco diferencial %s. Lo que se haya cambiado en la plantilla después de convertir el disco a multiconexión vive ahí y las instancias NO lo heredan.", attached)
	}
	if st, err := os.Stat(baseDisk); err != nil {
		return "", fmt.Errorf("el disco de la plantilla %s no está accesible: %w", baseDisk, err)
	} else if st.IsDir() {
		return "", fmt.Errorf("el disco de la plantilla %s es un directorio", baseDisk)
	}
	m.baseDisk = baseDisk

	if err := m.checkDNSReachable(ctx, rep); err != nil {
		return "", err
	}
	if _, err := exec.LookPath("nsupdate"); err != nil {
		return "", fmt.Errorf("nsupdate no está instalado en el host (paquete bind9-dnsutils): %w", err)
	}
	if key := strings.TrimSpace(m.opts.DNSKeyFile); key != "" {
		st, err := os.Stat(key)
		if err != nil {
			return "", fmt.Errorf("no se pudo leer la llave TSIG %s: %w", key, err)
		}
		if st.IsDir() {
			return "", fmt.Errorf("la llave TSIG %s es un directorio", key)
		}
		if st.Mode().Perm()&0077 != 0 {
			rep.Warn("La llave TSIG %s es accesible por grupo u otros; use chmod 600.", key)
		}
	} else {
		rep.Warn("No se configuró --httpaas-dns-key: nsupdate intentará una actualización sin TSIG. Úselo solo en un laboratorio.")
	}
	if strings.TrimSpace(m.opts.GuestPassword) == "" {
		rep.Warn("No se configuró contraseña para el usuario invitado; si la plantilla la exige, Guest Additions fallará.")
	}
	return baseDisk, nil
}

// checkDNSReachable asks the Bind9 server for the zone's NS records before
// anything is created, so an unreachable DNS fails in seconds instead of after
// the VM has been built (and rolled back).
func (m *HTTPaaSManager) checkDNSReachable(ctx context.Context, rep *op.Report) error {
	server := net.JoinHostPort(m.opts.DNSServer, "53")
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, server)
		},
	}
	qctx, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
	defer cancel()
	start := time.Now()
	ns, err := resolver.LookupNS(qctx, m.opts.DNSZone)
	if err != nil {
		return fmt.Errorf("el servidor DNS %s no responde por la zona %s (%v). "+
			"Compruebe que la VM del DNS esté encendida, que tenga la IP %s en la red %s y que named esté activo "+
			"(dig @%s %s SOA)", server, m.opts.DNSZone, err, m.opts.DNSServer, m.opts.NetworkName, m.opts.DNSServer, m.opts.DNSZone)
	}
	names := make([]string, 0, len(ns))
	for _, n := range ns {
		names = append(names, n.Host)
	}
	rep.Note("Bind9 en %s responde por %s (NS %s, %s).", server, m.opts.DNSZone, strings.Join(names, ", "),
		time.Since(start).Round(time.Millisecond))
	return nil
}

func (m *HTTPaaSManager) createDisk(ctx context.Context, rep *op.Report, inst HTTPaaSInstance) error {
	if _, err := os.Stat(inst.Disk); err == nil {
		return fmt.Errorf("el disco %s ya existe; elimínelo o revise el inventario", inst.Disk)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("comprobar disco %s: %w", inst.Disk, err)
	}
	// VirtualBox 7 has no "diff" medium type: a differencing disk is a
	// "disk" with --diffparent.
	args := []string{"createmedium", "disk", "--filename", inst.Disk, "--diffparent", inst.ParentDisk, "--format", "VDI"}
	if _, ok := m.vboxRequire(ctx, rep, "Crear disco diferencial para "+inst.Hostname, args...); !ok {
		return fmt.Errorf("no se pudo crear el disco diferencial sobre %s", inst.ParentDisk)
	}
	return nil
}

// vboxMedium is the subset of `VBoxManage showmediuminfo` used by the module.
type vboxMedium struct {
	UUID, Parent, State, Type, Location, AccessError string
}

func parseMediumInfo(out string) vboxMedium {
	var md vboxMedium
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "UUID":
			md.UUID = value
		case "Parent UUID":
			md.Parent = value
		case "State":
			md.State = value
		case "Type":
			md.Type = value
		case "Location":
			md.Location = value
		case "Access Error":
			md.AccessError = value
		}
	}
	return md
}

// resolveBaseDisk follows the parent chain of disk up to its root and checks
// that the root is an accessible multiattach medium.
func (m *HTTPaaSManager) resolveBaseDisk(ctx context.Context, rep *op.Report, disk string) (string, error) {
	cur := disk
	var md vboxMedium
	for depth := 0; ; depth++ {
		if depth > 32 {
			return "", fmt.Errorf("la cadena de discos de %s es demasiado larga", disk)
		}
		res := m.host.Run(ctx, nil, m.opts.VBoxManage, "showmediuminfo", "disk", cur)
		rep.Step("Inspeccionar disco "+cur, res)
		if !res.Ok() {
			return "", fmt.Errorf("no se pudo inspeccionar el disco %s: %s", cur, res.ErrorString())
		}
		md = parseMediumInfo(res.Stdout)
		if md.Parent == "" || strings.EqualFold(md.Parent, "base") {
			break
		}
		cur = md.Parent
	}
	if md.Location == "" {
		return "", fmt.Errorf("VirtualBox no informó la ubicación del disco base de %s", disk)
	}
	if strings.EqualFold(md.State, "inaccessible") {
		return "", fmt.Errorf("el disco multiconexión %s está registrado en VirtualBox pero no es accesible (%s). "+
			"El archivo fue movido o borrado: restáurelo en esa ruta o vuelva a crear la plantilla (docs/httpaas.md, «Recrear la plantilla»)",
			md.Location, firstNonEmpty(md.AccessError, "estado inaccessible"))
	}
	if !strings.HasPrefix(strings.ToLower(md.Type), "multiattach") {
		return "", fmt.Errorf("el disco base %s es de tipo %q y debe ser multiconexión. Con la plantilla apagada: "+
			"desconéctelo (storageattach --medium none), ejecute VBoxManage modifymedium disk %q --type multiattach y vuelva a conectarlo",
			md.Location, md.Type, md.Location)
	}
	rep.Note("Disco multiconexión de la plantilla: %s", md.Location)
	return md.Location, nil
}

func (m *HTTPaaSManager) createVM(ctx context.Context, rep *op.Report, inst HTTPaaSInstance) (bool, error) {
	if _, ok := m.vboxRequire(ctx, rep, "Crear máquina virtual "+inst.VM,
		"createvm", "--name", inst.VM, "--ostype", "Debian_64", "--register"); !ok {
		return false, fmt.Errorf("no se pudo crear la máquina virtual")
	}
	created := true

	nic := []string{"--nic1", "hostonly", "--hostonlyadapter1", m.opts.NetworkName, "--cableconnected1", "on"}
	if m.opts.NetworkMode == "bridged" {
		nic = []string{"--nic1", "bridged", "--bridgeadapter1", m.opts.NetworkName, "--cableconnected1", "on"}
	}
	modify := []string{
		"modifyvm", inst.VM,
		"--memory", fmt.Sprintf("%d", m.opts.MemoryMB),
		"--cpus", fmt.Sprintf("%d", m.opts.CPUs),
		"--ioapic", "on",
		"--vram", "16",
		"--boot1", "disk", "--boot2", "none", "--boot3", "none", "--boot4", "none",
		"--vrde", "off",
	}
	modify = append(modify, nic...)
	if _, ok := m.vboxRequire(ctx, rep, "Configurar red y recursos de "+inst.VM, modify...); !ok {
		return created, fmt.Errorf("no se pudo configurar la máquina virtual")
	}

	if _, ok := m.vboxRequire(ctx, rep, "Agregar controlador SATA a "+inst.VM,
		"storagectl", inst.VM, "--name", "SATA", "--add", "sata", "--controller", "IntelAhci",
		"--portcount", "4", "--bootable", "on"); !ok {
		return created, fmt.Errorf("no se pudo agregar el controlador SATA")
	}
	if _, ok := m.vboxRequire(ctx, rep, "Conectar disco diferencial a "+inst.VM,
		"storageattach", inst.VM, "--storagectl", "SATA", "--port", "0", "--device", "0",
		"--type", "hdd", "--medium", inst.Disk); !ok {
		return created, fmt.Errorf("no se pudo conectar el disco diferencial")
	}
	return created, nil
}

// waitGuest waits for the Guest Additions channel. The VM has no usable IP yet:
// the static address is configured through this same channel.
func (m *HTTPaaSManager) waitGuest(ctx context.Context, rep *op.Report, vm string) (runner.Runner, error) {
	gc := runner.NewGuestControl(runner.GuestControlOptions{
		VBoxManage: m.opts.VBoxManage,
		VM:         vm,
		User:       m.opts.GuestUser,
		Password:   m.opts.GuestPassword,
		Sudo:       m.opts.GuestSudo,
		Timeout:    m.opts.BootTimeout,
	})
	deadline := time.Now().Add(m.opts.BootTimeout)
	var lastErr error
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		err := gc.Ping(pingCtx)
		cancel()
		if err == nil {
			rep.Note("Guest Additions operativas en %s.", vm)
			return gc, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("Guest Additions no respondió en %s: %w", m.opts.BootTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(httpaaSGuestPoll):
		}
	}
}

func (m *HTTPaaSManager) configureGuest(ctx context.Context, rep *op.Report, gc runner.Runner, inst HTTPaaSInstance) error {
	if _, ok := m.guestRequire(ctx, rep, gc, "Asignar nombre de host "+inst.Hostname,
		"hostnamectl", "set-hostname", inst.Hostname); !ok {
		return errors.New("no se pudo asignar el nombre de host")
	}

	iface := strings.TrimSpace(m.opts.GuestInterface)
	if iface == "" {
		res := m.guestStep(ctx, rep, gc, "Detectar interfaz de red", "ip", "-o", "-4", "route", "show", "default")
		iface = parseDefaultInterface(res.Stdout)
		if iface == "" {
			res = m.guestStep(ctx, rep, gc, "Buscar interfaz de red", "ip", "-o", "link", "show")
			iface = parseFirstInterface(res.Stdout)
		}
	}
	if iface == "" {
		return errors.New("no se pudo detectar la interfaz de red del invitado; use --httpaas-guest-iface")
	}
	iface, err := validate.Identifier("la interfaz de red", iface)
	if err != nil {
		return err
	}

	if err := m.releaseFromNetworkManager(ctx, rep, gc, iface); err != nil {
		return err
	}
	interfaces := buildStaticInterfaces(iface, inst.IP, m.opts.Netmask, m.opts.Gateway, m.opts.DNSServer)
	if err := m.guestWriteFile(ctx, rep, gc, "/etc/network/interfaces.d/99-httpaas", []byte(interfaces), 0644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"ip", "addr", "flush", "dev", iface},
		{"ip", "link", "set", iface, "up"},
		{"ip", "addr", "add", fmt.Sprintf("%s/%d", inst.IP, m.opts.Netmask), "dev", iface},
		{"ip", "route", "replace", "default", "via", m.opts.Gateway, "dev", iface},
	} {
		if _, ok := m.guestRequire(ctx, rep, gc, strings.Join(args, " "), args[0], args[1:]...); !ok {
			return fmt.Errorf("no se pudo aplicar la configuración de red (%s)", strings.Join(args, " "))
		}
	}
	if err := m.guestWriteFile(ctx, rep, gc, "/etc/resolv.conf",
		[]byte("nameserver "+m.opts.DNSServer+"\noptions edns0 trust-ad\n"), 0644); err != nil {
		return err
	}
	rep.Note("Red configurada: %s/%d, puerta de enlace %s, DNS %s.", inst.IP, m.opts.Netmask, m.opts.Gateway, m.opts.DNSServer)

	hosts, found, err := readFile(ctx, gc, "/etc/hosts")
	if err != nil {
		return err
	}
	if found {
		updated := addHostEntry(UpdateHosts(string(hosts), inst.Hostname), inst.IP, inst.FQDN, inst.Hostname)
		if err := m.guestWriteFile(ctx, rep, gc, "/etc/hosts", []byte(updated), 0644); err != nil {
			return err
		}
	}
	if err := m.ensureApache(ctx, rep, gc); err != nil {
		return err
	}

	// The document root must exist before configtest, or Apache warns about it.
	if _, ok := m.guestRequire(ctx, rep, gc, "Crear directorio web "+inst.DocumentRoot,
		"install", "-d", "-o", "www-data", "-g", "www-data", "-m", "0755", inst.DocumentRoot); !ok {
		return errors.New("no se pudo crear el directorio web")
	}
	vhost := buildVHost(inst.Hostname, inst.FQDN, inst.DocumentRoot)
	if err := m.guestWriteFile(ctx, rep, gc, "/etc/apache2/sites-available/"+inst.Hostname+".conf", []byte(vhost), 0644); err != nil {
		return err
	}
	// 000-default is only informational: a template may not have it enabled.
	// A non-zero result is a valid answer here, so it must not fail the report.
	rep.OptionalStep("Deshabilitar sitio por omisión",
		gc.Run(ctx, nil, "a2dissite", "000-default"))
	if _, ok := m.guestRequire(ctx, rep, gc, "Habilitar sitio "+inst.Hostname, "a2ensite", inst.Hostname); !ok {
		return errors.New("no se pudo habilitar el sitio de Apache")
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Validar configuración de Apache", "apache2ctl", "configtest"); !ok {
		return errors.New("la configuración de Apache no es válida")
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Asegurar servicio Apache", "systemctl", "enable", "--now", "apache2"); !ok {
		return errors.New("no se pudo iniciar Apache")
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Recargar Apache", "systemctl", "reload", "apache2"); !ok {
		return errors.New("no se pudo recargar Apache")
	}
	return nil
}

// releaseFromNetworkManager stops NetworkManager (present on templates
// installed with a desktop) from managing the instance interface. Otherwise it
// keeps running DHCP on the host-only network, which has no DHCP server, and
// minutes later its retry cycle flushes the static address set by the module.
func (m *HTTPaaSManager) releaseFromNetworkManager(ctx context.Context, rep *op.Report, gc runner.Runner, iface string) error {
	probe := gc.Run(ctx, nil, "systemctl", "is-active", "--quiet", "NetworkManager")
	rep.OptionalStep("Detectar NetworkManager", probe)
	if !probe.Ok() {
		return nil
	}
	rep.Note("NetworkManager está activo en la instancia: %s pasa a ser no administrada para conservar la IP estática.", iface)
	if err := m.guestWriteFile(ctx, rep, gc, "/etc/NetworkManager/conf.d/99-httpaas.conf",
		[]byte(buildNMUnmanagedConf(iface)), 0644); err != nil {
		return err
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Recargar configuración de NetworkManager",
		"systemctl", "reload", "NetworkManager"); !ok {
		return errors.New("no se pudo recargar NetworkManager")
	}
	// The reload makes the setting persistent; this applies it right away.
	if _, ok := m.guestRequire(ctx, rep, gc, "Liberar "+iface+" de NetworkManager",
		"nmcli", "device", "set", iface, "managed", "no"); !ok {
		return errors.New("no se pudo liberar la interfaz de NetworkManager")
	}
	return nil
}

func (m *HTTPaaSManager) ensureApache(ctx context.Context, rep *op.Report, gc runner.Runner) error {
	// Check real paths instead of `command -v`: that is a shell builtin and
	// would not work behind the transport's `sudo -n` prefix.
	missing := false
	for _, bin := range []string{"/usr/sbin/apache2", "/usr/sbin/apache2ctl", "/usr/bin/unzip"} {
		res := gc.Run(ctx, nil, "test", "-x", bin)
		rep.OptionalStep("Comprobar "+bin, res)
		if !res.Ok() {
			missing = true
		}
	}
	if !missing {
		return nil
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Actualizar índices de apt",
		"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"); !ok {
		return errors.New("no se pudo actualizar apt dentro de la instancia")
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Instalar Apache y unzip",
		"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends", "apache2", "unzip"); !ok {
		return errors.New("no se pudo instalar Apache/unzip dentro de la instancia")
	}
	return nil
}

func (m *HTTPaaSManager) deployContent(ctx context.Context, rep *op.Report, gc runner.Runner, inst HTTPaaSInstance, zipData []byte) error {
	if _, ok := m.guestRequire(ctx, rep, gc, "Preparar directorio web "+inst.DocumentRoot,
		"install", "-d", "-o", "www-data", "-g", "www-data", "-m", "0755", inst.DocumentRoot); !ok {
		return errors.New("no se pudo preparar el directorio web")
	}
	// Clear previous content so a redeploy cannot mix old files. The directory
	// itself is controlled by the console (validated absolute path).
	if _, ok := m.guestRequire(ctx, rep, gc, "Limpiar contenido anterior",
		"find", inst.DocumentRoot, "-mindepth", "1", "-delete"); !ok {
		return errors.New("no se pudo limpiar el directorio web anterior")
	}

	tmp := path.Join("/tmp", "np2-"+inst.Hostname+"-"+randomToken()+".zip")
	if err := m.guestWriteFile(ctx, rep, gc, tmp, zipData, 0600); err != nil {
		return err
	}
	defer func() {
		_ = gc.Run(context.Background(), nil, "rm", "-f", "--", tmp)
	}()
	if _, ok := m.guestRequire(ctx, rep, gc, "Extraer contenido en "+inst.DocumentRoot,
		"unzip", "-o", "-q", tmp, "-d", inst.DocumentRoot); !ok {
		return errors.New("no se pudo extraer el archivo zip")
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Asignar propietario www-data",
		"chown", "-R", "www-data:www-data", inst.DocumentRoot); !ok {
		return errors.New("no se pudo ajustar el propietario del contenido")
	}
	// An absent index is not fatal (the zip may be a single-page app with a
	// different entry point), but it is almost always a deployment mistake.
	indexFound := false
	for _, name := range []string{"index.html", "index.htm", "index.php"} {
		if res := gc.Run(ctx, nil, "test", "-f", path.Join(inst.DocumentRoot, name)); res.Ok() {
			indexFound = true
			break
		}
	}
	if !indexFound {
		rep.Warn("El zip no contiene index.html, index.htm ni index.php en la raíz; Apache podría mostrar el listado o un 404.")
	}
	return nil
}

// deployPlaceholder writes a minimal index so the dashboard link works right
// after "Aceptar", before the user publishes the zip.
func (m *HTTPaaSManager) deployPlaceholder(ctx context.Context, rep *op.Report, gc runner.Runner, inst HTTPaaSInstance) error {
	page := buildPlaceholderPage(inst.FQDN, inst.Address())
	if err := m.guestWriteFile(ctx, rep, gc, path.Join(inst.DocumentRoot, "index.html"), []byte(page), 0644); err != nil {
		return err
	}
	if _, ok := m.guestRequire(ctx, rep, gc, "Asignar propietario www-data",
		"chown", "-R", "www-data:www-data", inst.DocumentRoot); !ok {
		return errors.New("no se pudo ajustar el propietario del directorio web")
	}
	return nil
}

func (m *HTTPaaSManager) registerDNS(ctx context.Context, rep *op.Report, fqdn, ip string) error {
	return m.dnsUpdate(ctx, rep, "Registrar "+fqdn+" en Bind9",
		buildDNSUpdate(m.opts.DNSServer, m.opts.DNSZone, fqdn, ip, m.opts.DNSTTL, false))
}

func (m *HTTPaaSManager) removeDNS(ctx context.Context, rep *op.Report, fqdn string) error {
	return m.dnsUpdate(ctx, rep, "Eliminar "+fqdn+" de Bind9",
		buildDNSUpdate(m.opts.DNSServer, m.opts.DNSZone, fqdn, "", 0, true))
}

func (m *HTTPaaSManager) dnsUpdate(ctx context.Context, rep *op.Report, title, script string) error {
	var args []string
	if key := strings.TrimSpace(m.opts.DNSKeyFile); key != "" {
		args = append(args, "-k", key)
	}
	res := m.host.Run(ctx, []byte(script), "nsupdate", args...)
	rep.Step(title, res)
	if !res.Ok() {
		return fmt.Errorf("%s: %s", title, firstNonEmpty(res.ErrorString(), fmt.Sprintf("nsupdate terminó con código %d", res.ExitCode)))
	}
	return nil
}

func (m *HTTPaaSManager) rollback(ctx context.Context, rep *op.Report, vmCreated, dnsAdded bool, inst HTTPaaSInstance) {
	if dnsAdded {
		if err := m.dnsUpdate(ctx, rep, "Deshacer registro DNS de "+inst.FQDN,
			buildDNSUpdate(m.opts.DNSServer, m.opts.DNSZone, inst.FQDN, "", 0, true)); err != nil {
			rep.Warn("No se pudo deshacer el DNS: %v", err)
		}
	}
	if vmCreated {
		m.host.Run(ctx, nil, m.opts.VBoxManage, "controlvm", inst.VM, "poweroff")
		res := m.host.Run(ctx, nil, m.opts.VBoxManage, "unregistervm", inst.VM, "--delete")
		rep.OptionalStep("Deshacer máquina virtual "+inst.VM, res)
		if !res.Ok() {
			// The disk may still be attached; deleting it here would leave a
			// registered VM pointing to a missing medium.
			rep.Warn("No se pudo deshacer la máquina virtual %s: %s", inst.VM, res.ErrorString())
			return
		}
	}
	if err := m.removeDisk(ctx, inst.Disk); err != nil {
		rep.Warn("No se pudo borrar el disco %s: %v", inst.Disk, err)
	}
}

// removeDisk deletes a differencing disk both from VirtualBox's media registry
// and from the filesystem. It is only called when the disk is not attached.
func (m *HTTPaaSManager) removeDisk(ctx context.Context, disk string) error {
	if strings.TrimSpace(disk) == "" {
		return nil
	}
	if err := m.checkNotProtectedDisk(disk); err != nil {
		return err
	}
	rel, err := filepath.Rel(m.opts.DiskDir, filepath.Clean(disk))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("el disco %s no está dentro de %s", disk, m.opts.DiskDir)
	}
	// closemedium unregisters the medium and removes its file. If it is not in
	// the registry (for example after unregistervm --delete), fall back to the
	// filesystem.
	if res := m.host.Run(ctx, nil, m.opts.VBoxManage, "closemedium", "disk", disk, "--delete"); !res.Ok() {
		if err := os.Remove(disk); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("VirtualBox no pudo cerrar el disco (%s) y el borrado directo falló: %w", res.ErrorString(), err)
		}
	}
	return nil
}

func (m *HTTPaaSManager) checkNotProtectedDisk(disk string) error {
	if strings.TrimSpace(disk) == "" {
		return nil
	}
	protected := []string{m.baseDisk, m.opts.TemplateDisk, m.templateChild}
	if m.baseDisk == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if attached, err := m.templateDisk(ctx); err == nil {
			protected = append(protected, attached)
		}
	}
	for _, p := range protected {
		if p != "" && samePath(disk, p) {
			return fmt.Errorf("el disco %s pertenece a la plantilla y está protegido", disk)
		}
	}
	return nil
}

func (m *HTTPaaSManager) templateDisk(ctx context.Context) (string, error) {
	res := m.host.Run(ctx, nil, m.opts.VBoxManage, "showvminfo", m.opts.TemplateVM, "--machinereadable")
	if !res.Ok() {
		return "", fmt.Errorf("consultar plantilla: %s", res.ErrorString())
	}
	disk := discoverTemplateDisk(res.Stdout)
	if disk == "" {
		return "", errors.New("la plantilla no tiene un disco SATA")
	}
	return disk, nil
}

func (m *HTTPaaSManager) vboxStep(ctx context.Context, rep *op.Report, title string, args ...string) runner.Result {
	res := m.host.Run(ctx, nil, m.opts.VBoxManage, args...)
	rep.Step(title, res)
	return res
}

func (m *HTTPaaSManager) vboxRequire(ctx context.Context, rep *op.Report, title string, args ...string) (runner.Result, bool) {
	res := m.vboxStep(ctx, rep, title, args...)
	if res.Ok() {
		return res, true
	}
	rep.Failf("%s: %s", title, firstNonEmpty(res.ErrorString(), fmt.Sprintf("el comando terminó con código %d", res.ExitCode)))
	return res, false
}

func (m *HTTPaaSManager) guestStep(ctx context.Context, rep *op.Report, gc runner.Runner, title string, name string, args ...string) runner.Result {
	res := step(ctx, gc, rep, title, nil, name, args...)
	return res
}

func (m *HTTPaaSManager) guestRequire(ctx context.Context, rep *op.Report, gc runner.Runner, title string, name string, args ...string) (runner.Result, bool) {
	res := m.guestStep(ctx, rep, gc, title, name, args...)
	if res.Ok() {
		return res, true
	}
	rep.Failf("%s: %s", title, firstNonEmpty(res.ErrorString(), fmt.Sprintf("el comando terminó con código %d", res.ExitCode)))
	return res, false
}

func (m *HTTPaaSManager) guestWriteFile(ctx context.Context, rep *op.Report, gc runner.Runner, target string, data []byte, mode uint32) error {
	if err := writeFile(ctx, gc, rep, target, data, mode); err != nil {
		return fmt.Errorf("escribir %s: %w", target, err)
	}
	return nil
}

func maxUncompressedFor(maxUploadMiB int) int64 {
	limit := int64(maxUploadMiB) << 20 * 6
	if limit < httpaaSMaxUncompressed {
		return httpaaSMaxUncompressed
	}
	return limit
}

// validateZipPayload checks the central directory before any byte reaches the
// guest. It rejects absolute paths, traversal, backslashes, NUL bytes and
// symbolic links, the classic ways to escape an Apache document root.
func validateZipPayload(data []byte, maxUncompressed int64, maxEntries int) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("el archivo no es un zip válido: %w", err)
	}
	if len(zr.File) == 0 {
		return errors.New("el zip está vacío")
	}
	if len(zr.File) > maxEntries {
		return fmt.Errorf("el zip contiene demasiados archivos (%d; máximo %d)", len(zr.File), maxEntries)
	}
	var total int64
	for _, f := range zr.File {
		name := f.Name
		if name == "" {
			return errors.New("el zip contiene una entrada sin nombre")
		}
		if strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") {
			return fmt.Errorf("entrada de zip no permitida: %q", name)
		}
		clean := path.Clean(name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("el zip intenta salir del directorio web: %q", name)
		}
		if strings.Contains(clean, "/../") || !strings.HasPrefix(name, clean) && clean != "." {
			return fmt.Errorf("ruta de zip ambigua: %q", name)
		}
		if clean == "." && !strings.HasSuffix(name, "/") {
			return fmt.Errorf("entrada de zip no permitida: %q", name)
		}
		mode := f.FileInfo().Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("el zip contiene un enlace simbólico (%q); no se permite por seguridad", name)
		}
		if !mode.IsRegular() && !mode.IsDir() {
			return fmt.Errorf("el zip contiene un tipo de archivo especial (%q)", name)
		}
		if f.UncompressedSize64 > uint64(maxUncompressed) {
			return fmt.Errorf("la entrada %q supera el tamaño máximo descomprimido", name)
		}
		total += int64(f.UncompressedSize64)
		if total > maxUncompressed {
			return fmt.Errorf("el contenido descomprimido supera el máximo permitido (%d MiB)", maxUncompressed>>20)
		}
	}
	return nil
}

// buildDNSUpdate returns the nsupdate script. Every value must have been
// validated before this function is called; it is deliberately not a shell
// command, just the protocol accepted by nsupdate on standard input.
// flattenZipRoot removes a single top-level folder wrapping every entry (what
// you get when compressing a directory), so index.html ends up in the document
// root. macOS metadata (__MACOSX/, .DS_Store) is dropped. It also lists hidden
// top-level names (.git, .env…) found anywhere in the archive.
func flattenZipRoot(data []byte) ([]byte, string, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", nil, fmt.Errorf("el archivo no es un zip válido: %w", err)
	}
	isJunk := func(name string) bool {
		return strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store"
	}

	hiddenSet := map[string]bool{}
	root := ""
	single := true
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		if isJunk(name) {
			continue
		}
		for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
			if strings.HasPrefix(part, ".") && part != "." && part != ".well-known" {
				hiddenSet[part] = true
			}
		}
		first, _, nested := strings.Cut(name, "/")
		if !nested {
			single = false // a file at the root: nothing to flatten
			continue
		}
		if root == "" {
			root = first
		} else if root != first {
			single = false
		}
	}
	hidden := make([]string, 0, len(hiddenSet))
	for h := range hiddenSet {
		hidden = append(hidden, h)
	}
	sort.Strings(hidden)
	if !single || root == "" {
		return data, "", hidden, nil
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := root + "/"
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		if isJunk(name) || name == prefix {
			continue
		}
		hdr := f.FileHeader
		hdr.Name = strings.TrimPrefix(name, prefix)
		raw, err := f.OpenRaw()
		if err != nil {
			return nil, "", nil, fmt.Errorf("leer %s del zip: %w", f.Name, err)
		}
		w, err := zw.CreateRaw(&hdr)
		if err != nil {
			return nil, "", nil, fmt.Errorf("reescribir %s: %w", f.Name, err)
		}
		if _, err := io.Copy(w, raw); err != nil {
			return nil, "", nil, fmt.Errorf("copiar %s: %w", f.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", nil, fmt.Errorf("cerrar zip: %w", err)
	}
	return buf.Bytes(), root, hidden, nil
}

func buildDNSUpdate(server, zone, fqdn, ip string, ttl int, remove bool) string {
	server = strings.TrimSpace(server)
	zone = strings.TrimSuffix(zone, ".") + "."
	fqdn = strings.TrimSuffix(fqdn, ".") + "."
	var b strings.Builder
	fmt.Fprintf(&b, "server %s\n", server)
	fmt.Fprintf(&b, "zone %s\n", zone)
	// Delete first so a repeated provision replaces a stale A record instead of
	// accumulating one. nsupdate ignores a delete for a record that is absent.
	fmt.Fprintf(&b, "update delete %s A\n", fqdn)
	if !remove {
		if ttl <= 0 {
			ttl = 300
		}
		fmt.Fprintf(&b, "update add %s %d A %s\n", fqdn, ttl, ip)
	}
	b.WriteString("send\n")
	return b.String()
}

func buildNMUnmanagedConf(iface string) string {
	return `# Generado por appgo HTTPaaS: la IP estática la gestiona ifupdown.
[main]
dns=none

[keyfile]
unmanaged-devices=interface-name:` + iface + "\n"
}

func buildStaticInterfaces(iface, ip string, prefix int, gateway, dns string) string {
	mask := net.IPMask(net.CIDRMask(prefix, 32))
	return fmt.Sprintf(`# Generado por appgo HTTPaaS. No editar a mano.
auto %s
iface %s inet static
    address %s
    netmask %s
    gateway %s
    dns-nameservers %s
`, iface, iface, ip, net.IP(mask).String(), gateway, dns)
}

func buildPlaceholderPage(fqdn, address string) string {
	fqdn, address = html.EscapeString(fqdn), html.EscapeString(address)
	return `<!doctype html>
<html lang="es"><head><meta charset="utf-8"><title>` + fqdn + `</title>
<style>body{font-family:system-ui,sans-serif;background:#f4f6fa;color:#1d2531;display:grid;place-items:center;min-height:100vh;margin:0}main{background:#fff;padding:32px 40px;border-radius:12px;box-shadow:0 2px 12px #0001;max-width:520px}code{background:#eef2f8;padding:2px 6px;border-radius:4px}</style>
</head><body><main><h1>` + fqdn + `</h1>
<p>La instancia Apache está lista en <code>` + address + `</code>.</p>
<p>Todavía no se ha publicado contenido. Desde la consola HTTPaaS seleccione el archivo <code>.zip</code> y pulse <strong>Publicar</strong>.</p>
</main></body></html>
`
}

func buildVHost(hostname, fqdn, documentRoot string) string {
	return fmt.Sprintf(`# Generado por appgo HTTPaaS.
<VirtualHost *:80>
    ServerName %s
    ServerAlias %s
    DocumentRoot %s

    <Directory %s>
        Options -Indexes +FollowSymLinks
        AllowOverride All
        Require all granted
    </Directory>

    # Nunca publicar repositorios ni archivos ocultos que vengan en el zip.
    <DirectoryMatch "/\.(git|svn|hg)(/|$)">
        Require all denied
    </DirectoryMatch>
    <FilesMatch "^\.">
        Require all denied
    </FilesMatch>

    ErrorLog ${APACHE_LOG_DIR}/%s-error.log
    CustomLog ${APACHE_LOG_DIR}/%s-access.log combined
</VirtualHost>
`, fqdn, hostname, documentRoot, documentRoot, hostname, hostname)
}

func addHostEntry(content, ip, fqdn, hostname string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			out = append(out, line)
			continue
		}
		fields := strings.Fields(trim)
		if len(fields) >= 1 && fields[0] == ip {
			continue
		}
		hasFQDN := false
		for _, f := range fields[1:] {
			if f == fqdn {
				hasFQDN = true
				break
			}
		}
		if hasFQDN {
			continue
		}
		out = append(out, line)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, fmt.Sprintf("%s\t%s %s", ip, fqdn, hostname), "")
	return strings.Join(out, "\n")
}

func parseDefaultInterface(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

func parseFirstInterface(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasSuffix(fields[0], ":") {
			name := strings.TrimSuffix(fields[1], ":")
			if name != "lo" && !strings.HasPrefix(name, "vbox") {
				return name
			}
		}
	}
	return ""
}

// listContainsInterface finds an entry in the output of `VBoxManage list
// hostonlyifs` or `list bridgedifs`, where each record starts with a Name field.
func listContainsInterface(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Name:") {
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(line, "Name:")) == name {
			return true
		}
	}
	return false
}

func parseMachineReadable(output string) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		m := reMachineProp.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) != 3 {
			continue
		}
		props[m[1]] = m[2]
	}
	return props
}

func discoverTemplateDisk(showVMInfo string) string {
	for _, line := range strings.Split(showVMInfo, "\n") {
		m := reVBoxSATA.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) != 2 {
			continue
		}
		disk := strings.TrimSpace(m[1])
		if disk == "" || disk == "none" || disk == "emptydrive" {
			continue
		}
		return disk
	}
	return ""
}

// vmNotFound recognises VirtualBox's "machine does not exist" diagnostic in
// English and in the localized messages used by this lab.
func vmNotFound(res runner.Result) bool {
	text := strings.ToLower(res.Stdout + "\n" + res.Stderr)
	for _, needle := range []string{
		"could not find a registered machine",
		"machine does not exist",
		"does not exist",
		"not found",
		"no se pudo encontrar",
		"no existe",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func statusFromVBoxState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running", "paused":
		return httpaaSStatusRunning
	case "poweroff", "aborted", "saved", "stopping":
		return httpaaSStatusStopped
	default:
		return httpaaSStatusUnknown
	}
}

func sameVM(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func nextFreeIP(start, end string, used map[string]bool, reserved ...string) (string, error) {
	first, err := ipv4ToUint(start)
	if err != nil {
		return "", err
	}
	last, err := ipv4ToUint(end)
	if err != nil {
		return "", err
	}
	if last < first {
		return "", errors.New("el rango de IP está invertido")
	}
	blocked := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		blocked[r] = true
	}
	for ip := first; ip <= last; ip++ {
		candidate := uint32ToIPv4(ip)
		if used[candidate] || blocked[candidate] {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no quedan direcciones libres en el rango %s – %s", start, end)
}

func ipv4ToUint(s string) (uint32, error) {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil || ip.To4() == nil {
		return 0, fmt.Errorf("IP inválida %q", s)
	}
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3]), nil
}

func uint32ToIPv4(v uint32) string {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
}

func randomToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
