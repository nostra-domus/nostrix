package main

import "net/http"

// captivePortalProbePaths are the well-known URLs each OS's network stack
// requests right after associating with a WiFi network, to detect whether
// the network requires a login/setup step before real internet access
// works. Each one normally expects a specific "you're online" response
// (an empty 204, or a fixed success page); responding with a redirect
// instead is what makes the OS pop its captive-portal mini-browser, pointed
// at the redirect target — the same trick real routers' setup portals use.
//
// DHCP option 114 (RFC 8910, set in modules/ap-portal.nix's dnsmasq config)
// covers newer clients that read the portal URL straight from the lease
// without probing at all; this is the fallback for clients that still probe.
//
// The mini-browser these open still goes through requireBasicAuth like the
// rest of bootstrap mode — same fixed credential, same trust level, just a
// second prompt inside the OS's captive-portal window.
var captivePortalProbePaths = []string{
	// Android / ChromeOS
	"/generate_204",
	"/gen_204",
	// Apple (iOS/macOS)
	"/hotspot-detect.html",
	"/library/test/success.html",
	// Windows
	"/connecttest.txt",
	"/ncsi.txt",
}

// registerCaptivePortalRoutes wires captivePortalProbePaths into mux,
// redirecting each to "/" — the same handler that serves the real bootstrap
// setup form.
func registerCaptivePortalRoutes(mux *http.ServeMux) {
	for _, path := range captivePortalProbePaths {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/", http.StatusFound)
		})
	}
}
