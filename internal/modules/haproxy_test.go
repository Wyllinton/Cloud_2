package modules

import "testing"

const haproxySample = `global
    log /dev/log local0
    daemon

defaults
    mode http
    timeout connect 5s

frontend http_front
    bind *:80
    default_backend app_backend

backend app_backend
    mode http
    balance roundrobin
    server srv1 192.168.1.11:8080 check
    server srv2 192.168.1.12:8080 check

backend stats_backend
    mode http
    server stats 192.168.1.13:8404
`

func TestParseBackends(t *testing.T) {
	backends := ParseBackends(haproxySample)
	if len(backends) != 2 {
		t.Fatalf("se esperaban 2 backends, se obtuvieron %d", len(backends))
	}
	if backends[0].Name != "app_backend" || len(backends[0].Servers) != 2 {
		t.Fatalf("app_backend mal analizado: %+v", backends[0])
	}
	if backends[0].Servers[1].Name != "srv2" || backends[0].Servers[1].Address != "192.168.1.12:8080" {
		t.Errorf("srv2 mal analizado: %+v", backends[0].Servers[1])
	}
	if backends[1].Name != "stats_backend" || len(backends[1].Servers) != 1 {
		t.Errorf("stats_backend mal analizado: %+v", backends[1])
	}
}

func TestAddServerExistingBackend(t *testing.T) {
	out, err := AddServer(haproxySample, AddServerRequest{
		BackendName: "app_backend",
		ServerName:  "srv3",
		Address:     "192.168.1.14",
		Port:        "8080",
		Check:       true,
		Weight:      "150",
	})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	backends := ParseBackends(out)
	var found bool
	for _, b := range backends {
		if b.Name != "app_backend" {
			continue
		}
		if len(b.Servers) != 3 {
			t.Fatalf("se esperaban 3 servidores, hay %d", len(b.Servers))
		}
		last := b.Servers[2]
		if last.Name != "srv3" || last.Address != "192.168.1.14:8080" {
			t.Errorf("servidor agregado incorrecto: %+v", last)
		}
		if last.Options != "check weight 150" {
			t.Errorf("opciones incorrectas: %q", last.Options)
		}
		found = true
	}
	if !found {
		t.Fatal("no se encontró el backend app_backend")
	}
	// El servidor debe quedar dentro del backend y no en stats_backend.
	if len(backends[1].Servers) != 1 {
		t.Errorf("stats_backend se modificó: %+v", backends[1])
	}
}

func TestAddServerDuplicate(t *testing.T) {
	_, err := AddServer(haproxySample, AddServerRequest{
		BackendName: "app_backend",
		ServerName:  "srv1",
		Address:     "192.168.1.99",
		Port:        "8080",
	})
	if err == nil {
		t.Fatal("se esperaba error por servidor duplicado")
	}
}

func TestAddServerMissingBackend(t *testing.T) {
	_, err := AddServer(haproxySample, AddServerRequest{
		BackendName: "nuevo",
		ServerName:  "srv1",
		Address:     "10.0.0.1",
		Port:        "80",
	})
	if err == nil {
		t.Fatal("se esperaba error porque el backend no existe")
	}
}

func TestAddServerCreatesBackend(t *testing.T) {
	out, err := AddServer(haproxySample, AddServerRequest{
		BackendName:   "nuevo",
		ServerName:    "srv1",
		Address:       "10.0.0.1",
		Port:          "80",
		CreateBackend: true,
		Mode:          "http",
		Balance:       "leastconn",
		Check:         true,
	})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	backends := ParseBackends(out)
	if len(backends) != 3 {
		t.Fatalf("se esperaban 3 backends, hay %d", len(backends))
	}
	last := backends[2]
	if last.Name != "nuevo" || len(last.Servers) != 1 || last.Servers[0].Address != "10.0.0.1:80" {
		t.Errorf("backend creado incorrecto: %+v", last)
	}
}

func TestRemoveServer(t *testing.T) {
	out, err := RemoveServer(haproxySample, RemoveServerRequest{BackendName: "app_backend", ServerName: "srv1"})
	if err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	backends := ParseBackends(out)
	if len(backends[0].Servers) != 1 || backends[0].Servers[0].Name != "srv2" {
		t.Errorf("srv1 no se eliminó correctamente: %+v", backends[0].Servers)
	}
}

func TestRemoveMissingServer(t *testing.T) {
	if _, err := RemoveServer(haproxySample, RemoveServerRequest{BackendName: "app_backend", ServerName: "nope"}); err == nil {
		t.Fatal("se esperaba error al eliminar un servidor inexistente")
	}
	if _, err := RemoveServer(haproxySample, RemoveServerRequest{BackendName: "nope", ServerName: "srv1"}); err == nil {
		t.Fatal("se esperaba error con un backend inexistente")
	}
}
