package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The curated app catalog. The Nix side is modules/catalog/*.nix, which
// declares `nostrix.catalog.<id>` with the options listed here; this file is
// what the web UI draws its forms from. The `catalogContract` flake check
// fails if the two drift apart.

//go:embed catalog.json
var catalogJSON []byte

// catalogEntry is one curated app.
type catalogEntry struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Note        string         `json:"note,omitempty"`
	Expose      []string       `json:"expose"` // exposure choices this app allows
	Fields      []catalogField `json:"fields"`
}

// catalogField is one app-specific setting. type is "text" or "bool"; values
// are kept as strings.
type catalogField struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Default string `json:"default"`
	Pattern string `json:"pattern,omitempty"`
	Help    string `json:"help,omitempty"`
}

// catalogApp is what the user chose for an installed catalog app.
type catalogApp struct {
	Expose   string            `json:"expose"`
	Settings map[string]string `json:"settings,omitempty"`
}

func loadCatalog() []catalogEntry {
	var entries []catalogEntry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		panic("catalog.json: " + err.Error())
	}
	return entries
}

func findCatalogEntry(id string) (catalogEntry, bool) {
	for _, e := range loadCatalog() {
		if e.ID == id {
			return e, true
		}
	}
	return catalogEntry{}, false
}

func (e catalogEntry) allowsExpose(x string) bool {
	for _, a := range e.Expose {
		if a == x {
			return true
		}
	}
	return false
}

// validateCatalogApp checks a user's choice against the catalog entry, so
// nothing unvetted reaches the generated flake.
func validateCatalogApp(id string, c catalogApp) error {
	e, ok := findCatalogEntry(id)
	if !ok {
		return fmt.Errorf("%q is not in the app catalog", id)
	}
	if !e.allowsExpose(c.Expose) {
		return fmt.Errorf("%s can't be exposed as %q", e.Name, c.Expose)
	}
	known := map[string]catalogField{}
	for _, f := range e.Fields {
		known[f.ID] = f
	}
	for k, v := range c.Settings {
		f, ok := known[k]
		if !ok {
			return fmt.Errorf("%s has no setting %q", e.Name, k)
		}
		switch f.Type {
		case "bool":
			if _, err := strconv.ParseBool(v); err != nil {
				return fmt.Errorf("%s: %s must be on or off", e.Name, f.Label)
			}
		default:
			if f.Pattern != "" && !regexp.MustCompile(f.Pattern).MatchString(v) {
				return fmt.Errorf("%s: %q is not a valid %s", e.Name, v, f.Label)
			}
		}
	}
	return nil
}

// sortedCatalogIDs returns the installed catalog apps in a stable order.
func sortedCatalogIDs(m map[string]catalogApp) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// catalogNix renders the `nostrix.catalog.<id>` block for an installed app.
func catalogNix(id string, c catalogApp) string {
	e, _ := findCatalogEntry(id)
	out := fmt.Sprintf("        {\n          nostrix.catalog.%s = {\n            enable = true;\n            expose = \"%s\";\n", id, escapeNixString(c.Expose))
	for _, f := range e.Fields {
		v, ok := c.Settings[f.ID]
		if !ok || (f.Type != "bool" && v == "") {
			continue // the module's default applies
		}
		if f.Type == "bool" {
			b, _ := strconv.ParseBool(v)
			out += fmt.Sprintf("            %s = %t;\n", f.ID, b)
		} else {
			out += fmt.Sprintf("            %s = \"%s\";\n", f.ID, escapeNixString(v))
		}
	}
	return out + "          };\n        }\n"
}

// ---- web UI -------------------------------------------------------------

var exposeLabels = map[string]string{
	"none":       "Only this device",
	"lan":        "My local network",
	"cloudflare": "The internet, behind my Cloudflare login",
}

type exposeChoice struct {
	Value, Label       string
	Selected, Disabled bool
}

type fieldView struct {
	catalogField
	Value   string
	Checked bool
}

// catalogView is one catalog card on the Apps page.
type catalogView struct {
	catalogEntry
	Installed bool
	Live      bool // the running system already declares it (apps.json)
	Choices   []exposeChoice
	Form      []fieldView
}

func catalogViews(s state, live map[string]bool) []catalogView {
	var views []catalogView
	for _, e := range loadCatalog() {
		cur, installed := s.Catalog[e.ID]
		v := catalogView{catalogEntry: e, Installed: installed, Live: live[e.ID]}
		for _, x := range e.Expose {
			v.Choices = append(v.Choices, exposeChoice{
				Value:    x,
				Label:    exposeLabels[x],
				Selected: x == cur.Expose,
				Disabled: x == "cloudflare" && s.CloudflareTunnelToken == "",
			})
		}
		for _, f := range e.Fields {
			val := f.Default
			if got, ok := cur.Settings[f.ID]; ok {
				val = got
			}
			fv := fieldView{catalogField: f, Value: val}
			fv.Checked, _ = strconv.ParseBool(val)
			v.Form = append(v.Form, fv)
		}
		views = append(views, v)
	}
	return views
}

// handleCatalog installs a catalog app or changes its settings.
func (srv *server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s, _ := loadState(srv.stateFile)
	id := r.FormValue("id")
	e, ok := findCatalogEntry(id)
	if !ok {
		http.Error(w, fmt.Sprintf("%q is not in the app catalog", id), http.StatusBadRequest)
		return
	}
	c := catalogApp{Expose: r.FormValue("expose"), Settings: map[string]string{}}
	if c.Expose == "cloudflare" && s.CloudflareTunnelToken == "" {
		http.Error(w, "Cloudflare remote access isn't set up on this device.", http.StatusBadRequest)
		return
	}
	for _, f := range e.Fields {
		if f.Type == "bool" {
			c.Settings[f.ID] = strconv.FormatBool(r.FormValue("f_"+f.ID) != "")
		} else {
			c.Settings[f.ID] = strings.TrimSpace(r.FormValue("f_" + f.ID))
		}
	}
	if err := validateCatalogApp(id, c); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.Catalog == nil {
		s.Catalog = map[string]catalogApp{}
	}
	s.Catalog[id] = c
	if err := srv.applyState(s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

// handleCatalogRemove switches a catalog app off. Its data stays on disk.
func (srv *server) handleCatalogRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s, _ := loadState(srv.stateFile)
	delete(s.Catalog, r.FormValue("id"))
	if err := srv.applyState(s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}
