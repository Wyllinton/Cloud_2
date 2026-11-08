package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"np2/appgo/internal/modules"
)

const maxUploadSize = 64 << 20 // 64 MiB

// --- dashboard ---------------------------------------------------------------

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	v := s.newView("dashboard", "Consola de automatización", "inicio")
	v.Flash = s.runner.Target()
	s.loadHTTPaaS(v)
	s.render(w, "dashboard", v)
}

// --- packages ----------------------------------------------------------------

func (s *Server) handlePackages(w http.ResponseWriter, r *http.Request) {
	v := s.newView("packages", "Instalación y eliminación de aplicaciones", "paquetes")
	ctx, cancel := s.opContext(r)
	defer cancel()
	if list, err := s.pkgs.Installed(ctx, 300); err != nil {
		v.Error = err.Error()
	} else {
		v.Installed = list
	}
	s.render(w, "packages", v)
}

func (s *Server) handlePackageInstall(w http.ResponseWriter, r *http.Request) {
	v := s.newView("packages", "Instalación de aplicaciones", "paquetes")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := modules.PackageRequest{
		Package:    r.FormValue("paquete"),
		Update:     r.FormValue("actualizar") != "",
		Autoremove: r.FormValue("autoremove") != "",
	}
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	v.Report = s.pkgs.Install(ctx, req)
	s.finalize(v)
	s.loadInstalled(ctx, v)
	s.render(w, "packages", v)
}

func (s *Server) handlePackageRemove(w http.ResponseWriter, r *http.Request) {
	v := s.newView("packages", "Eliminación de aplicaciones", "paquetes")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := modules.PackageRequest{
		Package:    r.FormValue("paquete"),
		Purge:      r.FormValue("purgar") != "",
		Autoremove: r.FormValue("autoremove") != "",
	}
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	v.Report = s.pkgs.Remove(ctx, req)
	s.finalize(v)
	s.loadInstalled(ctx, v)
	s.render(w, "packages", v)
}

func (s *Server) loadInstalled(ctx context.Context, v *view) {
	list, err := s.pkgs.Installed(ctx, 300)
	if err != nil {
		if v.Error == "" {
			v.Error = err.Error()
		}
		return
	}
	v.Installed = list
}

func (s *Server) loadServices(ctx context.Context, v *view) {
	list, err := s.systemd.List(ctx)
	if err != nil {
		if v.Error == "" {
			v.Error = err.Error()
		}
		return
	}
	v.Services = list
}

// --- root ssh ----------------------------------------------------------------

func (s *Server) handleRootSSH(w http.ResponseWriter, r *http.Request) {
	v := s.newView("rootssh", "Llave SSH de root", "ssh-root")
	ctx, cancel := s.opContext(r)
	defer cancel()
	if status, err := s.rootSSH.SSHStatus(ctx); err == nil {
		v.SSHStatus = status
	}
	s.render(w, "rootssh", v)
}

func (s *Server) handleRootSSHConfigure(w http.ResponseWriter, r *http.Request) {
	v := s.newView("rootssh", "Llave SSH de root", "ssh-root")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := modules.RootSSHRequest{
		Force:               r.FormValue("regenerar") != "",
		DisablePasswordAuth: r.FormValue("deshabilitar_password") != "",
		Comment:             r.FormValue("comentario"),
	}
	v.Report = s.rootSSH.Configure(ctx, req)
	s.finalize(v)
	if status, err := s.rootSSH.SSHStatus(ctx); err == nil {
		v.SSHStatus = status
	}
	s.render(w, "rootssh", v)
}

// --- issue -------------------------------------------------------------------

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	v := s.newView("issue", "Personalización del mensaje de inicio", "issue")
	ctx, cancel := s.opContext(r)
	defer cancel()
	if content, err := s.issue.Current(ctx); err != nil {
		v.Error = err.Error()
	} else {
		v.IssueContent = content
	}
	s.render(w, "issue", v)
}

func (s *Server) handleIssuePreview(w http.ResponseWriter, r *http.Request) {
	v := s.newView("issue", "Personalización del mensaje de inicio", "issue")
	req := issueRequest(r)
	preview, err := s.issue.Preview(req)
	if err != nil {
		v.Error = err.Error()
	} else {
		v.Preview = preview
	}
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	ctx, cancel := s.opContext(r)
	defer cancel()
	if content, err := s.issue.Current(ctx); err == nil {
		v.IssueContent = content
	}
	s.render(w, "issue", v)
}

func (s *Server) handleIssueApply(w http.ResponseWriter, r *http.Request) {
	v := s.newView("issue", "Personalización del mensaje de inicio", "issue")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := issueRequest(r)
	v.Report = s.issue.Apply(ctx, req)
	s.finalize(v)
	if content, err := s.issue.Current(ctx); err == nil {
		v.IssueContent = content
	}
	s.render(w, "issue", v)
}

func issueRequest(r *http.Request) modules.IssueRequest {
	return modules.IssueRequest{
		Hostname:     r.FormValue("mostrar_host") != "",
		Date:         r.FormValue("mostrar_fecha") != "",
		Time:         r.FormValue("mostrar_hora") != "",
		IPv4:         r.FormValue("mostrar_ip") != "",
		Kernel:       r.FormValue("mostrar_kernel") != "",
		OSName:       r.FormValue("mostrar_so") != "",
		TTY:          r.FormValue("mostrar_tty") != "",
		Custom:       r.FormValue("mensaje"),
		AlsoIssueNet: r.FormValue("tambien_issue_net") != "",
	}
}

// --- hostname ----------------------------------------------------------------

func (s *Server) handleHostname(w http.ResponseWriter, r *http.Request) {
	v := s.newView("hostname", "Nombre de host y /etc/hosts", "hostname")
	ctx, cancel := s.opContext(r)
	defer cancel()
	s.loadHostname(ctx, v)
	s.render(w, "hostname", v)
}

func (s *Server) handleHostnameSet(w http.ResponseWriter, r *http.Request) {
	v := s.newView("hostname", "Nombre de host y /etc/hosts", "hostname")
	ctx, cancel := s.opContext(r)
	defer cancel()

	v.Report = s.hostname.Set(ctx, modules.HostnameRequest{Name: r.FormValue("hostname")})
	s.finalize(v)
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	s.loadHostname(ctx, v)
	s.render(w, "hostname", v)
}

func (s *Server) loadHostname(ctx context.Context, v *view) {
	name, hosts, err := s.hostname.Current(ctx)
	if err != nil {
		if v.Error == "" {
			v.Error = err.Error()
		}
		return
	}
	v.CurrentHostname = name
	v.HostsContent = hosts
}

// --- users -------------------------------------------------------------------

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	v := s.newView("users", "Gestión de usuarios", "usuarios")
	ctx, cancel := s.opContext(r)
	defer cancel()
	s.loadUsers(ctx, v)
	s.render(w, "users", v)
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	v := s.newView("users", "Crear usuario", "usuarios")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := modules.CreateUserRequest{
		Username:   r.FormValue("usuario"),
		Sudo:       r.FormValue("sudo") != "",
		SSHKey:     r.FormValue("llave_ssh") != "",
		Shell:      r.FormValue("shell"),
		Comment:    r.FormValue("comentario"),
		CreateHome: r.FormValue("crear_home") != "",
	}
	v.Report = s.users.Create(ctx, req)
	s.finalize(v)
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	s.loadUsers(ctx, v)
	s.render(w, "users", v)
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	v := s.newView("users", "Eliminar usuario", "usuarios")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := modules.DeleteUserRequest{
		Username:   r.FormValue("usuario"),
		RemoveHome: r.FormValue("eliminar_archivos") != "",
		Terminate:  r.FormValue("finalizar_sesiones") != "",
	}
	v.Report = s.users.Delete(ctx, req)
	s.finalize(v)
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	s.loadUsers(ctx, v)
	s.render(w, "users", v)
}

func (s *Server) loadUsers(ctx context.Context, v *view) {
	list, err := s.users.List(ctx)
	if err != nil {
		if v.Error == "" {
			v.Error = err.Error()
		}
		return
	}
	v.Users = list
}

// --- services ----------------------------------------------------------------

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	v := s.newView("services", "Gestión de servicios systemd", "servicios")
	ctx, cancel := s.opContext(r)
	defer cancel()
	s.loadServices(ctx, v)
	s.render(w, "services", v)
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	v := s.newView("services", "Control de servicios systemd", "servicios")
	ctx, cancel := s.opContext(r)
	defer cancel()

	v.Report = s.systemd.Action(ctx, r.FormValue("servicio"), r.FormValue("accion"))
	s.finalize(v)
	s.loadServices(ctx, v)
	s.render(w, "services", v)
}

func (s *Server) handleServiceDeploy(w http.ResponseWriter, r *http.Request) {
	v := s.newView("services", "Desplegar servicio systemd", "servicios")
	ctx, cancel := s.opContext(r)
	defer cancel()

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		v.Error = "No se pudo procesar el formulario con archivos: " + err.Error()
		s.loadServices(ctx, v)
		s.render(w, "services", v)
		return
	}

	files, err := readUploads(r.MultipartForm)
	executable := r.FormValue("ejecutable") != ""
	if err == nil {
		for i := range files {
			files[i].Executable = executable
		}
	}

	req := modules.DeployRequest{
		ServiceName: r.FormValue("servicio"),
		Description: r.FormValue("descripcion"),
		WorkingDir:  r.FormValue("directorio"),
		ExecCommand: r.FormValue("comando"),
		ExecArgs:    r.FormValue("argumentos"),
		RunAs:       r.FormValue("usuario_ejecucion"),
		Environment: splitNonEmpty(r.FormValue("entorno")),
		Files:       files,
		Enable:      r.FormValue("habilitar") != "",
		Start:       r.FormValue("iniciar") != "",
		Restart:     r.FormValue("reiniciar") != "",
	}
	if err != nil {
		v.Error = err.Error()
		s.loadServices(ctx, v)
		s.render(w, "services", v)
		return
	}

	v.Report = s.systemd.Deploy(ctx, req)
	s.finalize(v)
	s.loadServices(ctx, v)
	s.render(w, "services", v)
}

func (s *Server) handleServiceRemove(w http.ResponseWriter, r *http.Request) {
	v := s.newView("services", "Eliminar servicio systemd", "servicios")
	ctx, cancel := s.opContext(r)
	defer cancel()

	v.Report = s.systemd.Remove(ctx, r.FormValue("servicio"))
	s.finalize(v)
	s.loadServices(ctx, v)
	s.render(w, "services", v)
}

func readUploads(form *multipart.Form) ([]modules.FileUpload, error) {
	if form == nil {
		return nil, nil
	}
	var out []modules.FileUpload
	for _, header := range form.File["archivos"] {
		f, err := header.Open()
		if err != nil {
			return nil, fmt.Errorf("no se pudo abrir %s: %w", header.Filename, err)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxUploadSize))
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("no se pudo leer %s: %w", header.Filename, err)
		}
		if len(data) == 0 {
			continue
		}
		out = append(out, modules.FileUpload{Name: header.Filename, Data: data})
	}
	return out, nil
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// --- haproxy -----------------------------------------------------------------

func (s *Server) handleHAProxy(w http.ResponseWriter, r *http.Request) {
	v := s.newView("haproxy", "Balanceador de carga HAProxy", "haproxy")
	ctx, cancel := s.opContext(r)
	defer cancel()
	s.loadHAProxy(ctx, v)
	s.render(w, "haproxy", v)
}

func (s *Server) handleHAProxyAdd(w http.ResponseWriter, r *http.Request) {
	v := s.newView("haproxy", "Agregar servidor al backend", "haproxy")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := modules.AddServerRequest{
		BackendName:   r.FormValue("backend"),
		ServerName:    r.FormValue("servidor"),
		Address:       r.FormValue("direccion"),
		Port:          r.FormValue("puerto"),
		Check:         r.FormValue("check") != "",
		Backup:        r.FormValue("backup") != "",
		Weight:        r.FormValue("peso"),
		CreateBackend: r.FormValue("crear_backend") != "",
		Mode:          r.FormValue("modo"),
		Balance:       r.FormValue("balanceo"),
	}
	v.Report = s.haproxy.Add(ctx, req)
	s.finalize(v)
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	s.loadHAProxy(ctx, v)
	s.render(w, "haproxy", v)
}

func (s *Server) handleHAProxyRemove(w http.ResponseWriter, r *http.Request) {
	v := s.newView("haproxy", "Eliminar servidor del backend", "haproxy")
	ctx, cancel := s.opContext(r)
	defer cancel()

	req := modules.RemoveServerRequest{
		BackendName: r.FormValue("backend"),
		ServerName:  r.FormValue("servidor"),
	}
	v.Report = s.haproxy.Remove(ctx, req)
	s.finalize(v)
	if err := r.ParseForm(); err == nil {
		v.Form = formValues(r)
	}
	s.loadHAProxy(ctx, v)
	s.render(w, "haproxy", v)
}

func (s *Server) loadHAProxy(ctx context.Context, v *view) {
	cfg, err := s.haproxy.CurrentContent(ctx)
	if err != nil {
		if v.Error == "" {
			v.Error = err.Error()
		}
		return
	}
	v.Backends = modules.ParseBackends(cfg)
}

// --- HTTPaaS ----------------------------------------------------------------

func (s *Server) handleHTTPaaS(w http.ResponseWriter, r *http.Request) {
	v := s.newView("httpaas", "HTTP como servicio (HTTPaaS)", "httpaas")
	ctx, cancel := s.opContext(r)
	defer cancel()
	if s.httpaas == nil {
		v.Error = "El módulo HTTPaaS está deshabilitado. Inicie la consola con -httpaas y configure la plantilla y el DNS."
		s.loadHTTPaaS(v)
		s.render(w, "httpaas", v)
		return
	}
	if err := s.httpaas.Refresh(ctx); err != nil && v.Error == "" {
		v.Error = err.Error()
	}
	s.loadHTTPaaS(v)
	s.render(w, "httpaas", v)
}

func (s *Server) handleHTTPaaSCreate(w http.ResponseWriter, r *http.Request) {
	v := s.newView("httpaas", "Aprovisionar instancia HTTPaaS", "httpaas")
	ctx, cancel := s.opContext(r)
	defer cancel()
	if s.httpaas == nil {
		v.Error = "El módulo HTTPaaS está deshabilitado."
		s.loadHTTPaaS(v)
		s.render(w, "httpaas", v)
		return
	}
	// "Aceptar" only needs the hostname; a zip attached at the same time is
	// deployed in the same operation.
	name, data, err := s.readZipUpload(w, r, false)
	v.Form = map[string]string{"hostname": r.FormValue("hostname")}
	if err != nil {
		v.Error = err.Error()
		s.loadHTTPaaS(v)
		s.render(w, "httpaas", v)
		return
	}
	v.Report = s.httpaas.Provision(ctx, modules.HTTPaaSProvisionRequest{
		Hostname: r.FormValue("hostname"),
		ZipName:  name,
		Zip:      data,
	})
	s.finalize(v)
	s.loadHTTPaaS(v)
	s.render(w, "httpaas", v)
}

func (s *Server) handleHTTPaaSPublish(w http.ResponseWriter, r *http.Request) {
	v := s.newView("httpaas", "Publicar contenido HTTPaaS", "httpaas")
	ctx, cancel := s.opContext(r)
	defer cancel()
	if s.httpaas == nil {
		v.Error = "El módulo HTTPaaS está deshabilitado."
		s.loadHTTPaaS(v)
		s.render(w, "httpaas", v)
		return
	}
	name, data, err := s.readZipUpload(w, r, true)
	v.Form = map[string]string{"hostname": r.FormValue("hostname")}
	if err != nil {
		v.Error = err.Error()
		s.loadHTTPaaS(v)
		s.render(w, "httpaas", v)
		return
	}
	v.Report = s.httpaas.Publish(ctx, modules.HTTPaaSPublishRequest{
		Hostname: r.FormValue("hostname"),
		ZipName:  name,
		Zip:      data,
	})
	s.finalize(v)
	s.loadHTTPaaS(v)
	s.render(w, "httpaas", v)
}

// readZipUpload parses the multipart form and returns the "archivo" field. The
// body is bounded before parsing; with required=false a missing file is not an
// error and returns empty values.
func (s *Server) readZipUpload(w http.ResponseWriter, r *http.Request, required bool) (string, []byte, error) {
	maxBytes := int64(s.cfg.HTTPaaS.MaxUploadMiB) << 20
	if maxBytes <= 0 {
		maxBytes = maxUploadSize
	}
	// Leave room for the multipart headers and the hostname field.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	if err := r.ParseMultipartForm(maxBytes + (1 << 20)); err != nil {
		return "", nil, fmt.Errorf("No se pudo procesar el formulario con el archivo .zip: %w", err)
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("archivo")
	if errors.Is(err, http.ErrMissingFile) || (err == nil && header.Size == 0 && header.Filename == "") {
		if required {
			return "", nil, errors.New("Seleccione el archivo .zip con el contenido web antes de pulsar «Publicar».")
		}
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("No se pudo leer el archivo adjunto: %w", err)
	}
	defer file.Close()

	name := filepath.Base(header.Filename)
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return "", nil, errors.New("El archivo debe tener extensión .zip.")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("No se pudo leer el archivo .zip: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return "", nil, fmt.Errorf("El archivo supera el máximo de %d MiB.", maxBytes>>20)
	}
	if len(data) == 0 {
		return "", nil, errors.New("El archivo .zip está vacío.")
	}
	return name, data, nil
}

func (s *Server) handleHTTPaaSDelete(w http.ResponseWriter, r *http.Request) {
	v := s.newView("httpaas", "Eliminar instancia HTTPaaS", "httpaas")
	ctx, cancel := s.opContext(r)
	defer cancel()
	if s.httpaas == nil {
		v.Error = "El módulo HTTPaaS está deshabilitado."
		s.loadHTTPaaS(v)
		s.render(w, "httpaas", v)
		return
	}
	v.Report = s.httpaas.Delete(ctx, r.FormValue("hostname"))
	s.finalize(v)
	s.loadHTTPaaS(v)
	s.render(w, "httpaas", v)
}

func (s *Server) loadHTTPaaS(v *view) {
	v.HTTPaaSEnabled = s.httpaas != nil
	if s.httpaas == nil {
		return
	}
	v.HTTPaaSDomain = s.httpaas.Domain()
	v.HTTPaaSIPRange = s.httpaas.IPRange()
	v.HTTPaaSTemplate = s.httpaas.Template()
	v.HTTPaaSInstances = s.httpaas.List()
}

// --- downloads ---------------------------------------------------------------

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	art, ok := s.artifacts.get(id)
	if !ok {
		http.Error(w, "El archivo ya no está disponible. Vuelva a ejecutar la operación.", http.StatusNotFound)
		return
	}
	filename := art.Filename
	if filename == "" {
		filename = "descarga"
	}
	w.Header().Set("Content-Type", art.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(filename)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(art.Data)
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "/", "_")
	return name
}

// --- shared helpers ----------------------------------------------------------

func formValues(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, vs := range r.Form {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}
