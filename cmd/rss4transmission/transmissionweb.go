package main

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

//go:embed web/transmission.html
var transmissionTmpl string

// proxyMethods are the HTTP methods the reverse proxy forwards. The web client
// needs GET for its assets and POST for the RPC endpoint. The rest are here so
// a browser preflight or a stray HEAD does not fall through to the history
// page's catch-all. Anything else gets a 405 from the mux.
var proxyMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodDelete, http.MethodOptions,
}

// transmissionOrigin builds the scheme://host:port that the reverse proxy
// forwards to. It is the same origin the RPC client uses, minus the RPC path,
// because the proxy forwards every path unchanged.
func transmissionOrigin(https bool, host string, port int) (*url.URL, error) {
	if host == "" {
		return nil, fmt.Errorf("Transmission.Host is empty")
	}
	proto := "http"
	if https {
		proto = "https"
	}
	return url.Parse(fmt.Sprintf("%s://%s:%d", proto, host, port))
}

// transmissionProxyTarget returns the origin the Transmission page must proxy
// to, or nil when the page is turned off or the config cannot produce a usable
// origin. A nil result means the routes are not registered and the nav item is
// not shown.
func transmissionProxyTarget(cfg Transmission) *url.URL {
	if !cfg.WebUI {
		return nil
	}
	target, err := transmissionOrigin(cfg.HTTPS, cfg.Host, cfg.Port)
	if err != nil {
		log.WithError(err).Warn("Transmission.WebUI is on but the Transmission page cannot be served")
		return nil
	}
	return target
}

// registerTransmissionRoutes adds the Transmission page and the reverse proxy
// that feeds its iframe.
//
//   - GET /transmission is the page: the shared nav bar plus a full-height
//     iframe.
//   - /transmission/ is the proxy, on every method, because the web client
//     sends POST /transmission/rpc.
//
// The proxy exists because the browser usually cannot reach Transmission
// itself: Transmission.Host is a Docker service name in the common
// deployments. Proxying also makes the frame same-origin and lets us attach
// the configured credentials, so the browser never prompts inside the frame.
//
// These routes are for the private mux only. Like GET /, they are
// unauthenticated, and they give their caller full control of Transmission.
//
// The two patterns do not collide: Go matches the exact "/transmission"
// pattern before the "/transmission/" subtree, so the page is not swallowed by
// the proxy.
//
// The proxy is registered once per method rather than as a bare
// "/transmission/" pattern. The history page owns "GET /", and Go rejects a
// pattern that matches more methods on a more specific path as a conflict.
func registerTransmissionRoutes(mux *http.ServeMux, target *url.URL, user, pass string, nav navConfig) {
	nav.Transmission = true
	funcs := nav.navFuncs()
	tmpl := template.Must(template.Must(
		template.New("transmission").Funcs(funcs).Parse(navTmpl)).Parse(transmissionTmpl))

	mux.HandleFunc("GET /transmission", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, nil); err != nil {
			log.WithError(err).Error("Failed to render transmission template")
		}
	})

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// SetURL joins target.Path onto the inbound path. target has no
			// path, so the inbound path reaches the upstream unchanged.
			pr.Out.Host = target.Host
			if user != "" {
				pr.Out.SetBasicAuth(user, pass)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			stripFramingHeaders(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.WithError(err).Warnf("Transmission proxy failed for %s", r.URL.Path)
			http.Error(w, "Transmission is unreachable", http.StatusBadGateway)
		},
	}
	for _, method := range proxyMethods {
		mux.Handle(method+" /transmission/", proxy)
	}
}

// stripFramingHeaders removes the two response headers that would stop the
// browser from showing Transmission inside our iframe. Other CSP directives
// are kept, so a policy that also restricts scripts still applies.
func stripFramingHeaders(h http.Header) {
	h.Del("X-Frame-Options")

	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		return
	}
	kept := []string{}
	for _, directive := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(directive)), "frame-ancestors") {
			continue
		}
		if strings.TrimSpace(directive) == "" {
			continue
		}
		kept = append(kept, strings.TrimSpace(directive))
	}
	if len(kept) == 0 {
		h.Del("Content-Security-Policy")
		return
	}
	h.Set("Content-Security-Policy", strings.Join(kept, "; "))
}
