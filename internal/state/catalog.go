package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

const (
	// DefaultClusterName is the name of the first demo's kind cluster.
	DefaultClusterName = "kubrix-demo"

	fileName = "installations.json"
)

var clusterNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// file is what is saved: the installations by key, and the keys most recently used first.
type file struct {
	Recent        []string                `json:"recent,omitempty"`
	Installations map[string]Installation `json:"installations,omitempty"`
}

// Catalog is the user's saved Installations: demos on kind clusters and installs on existing clusters.
//
// A Catalog that could not read its file is empty, reports why through Problem, and refuses to write, so the
// unreadable file is never overwritten.
type Catalog struct {
	path    string
	data    file
	problem error
}

// Open reads the catalog in dir. A missing file is an empty catalog; an empty dir is an unavailable one.
func Open(dir string) *Catalog {
	if dir == "" {
		return &Catalog{problem: errors.New("no directory to keep installations in")}
	}
	c := &Catalog{path: filepath.Join(dir, fileName)}
	c.data, c.problem = load(c.path)
	if c.problem != nil {
		c.data = file{}
	}
	return c
}

// Problem is why the saved installations could not be read, or nil.
func (c *Catalog) Problem() error { return c.problem }

func load(path string) (file, error) {
	var f file
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

// Len is the number of saved installations.
func (c *Catalog) Len() int { return len(c.data.Installations) }

// Find returns the installation with the key (kind cluster name or kube context).
func (c *Catalog) Find(key string) (Installation, bool) {
	st, ok := c.data.Installations[key]
	return st, ok
}

// Keys returns the keys of all installations, most recently used first.
func (c *Catalog) Keys() []string { return c.keys(func(Installation) bool { return true }) }

// Demos returns the keys of the demos, most recently used first.
func (c *Catalog) Demos() []string {
	return c.keys(func(st Installation) bool { return !st.IsCluster() })
}

// Clusters returns the keys of the installs on existing clusters, most recently used first.
func (c *Catalog) Clusters() []string { return c.keys(Installation.IsCluster) }

func (c *Catalog) keys(keep func(Installation) bool) []string {
	var out []string
	for _, key := range c.data.Recent {
		if st, ok := c.data.Installations[key]; ok && keep(st) {
			out = append(out, key)
		}
	}
	var rest []string
	for key, st := range c.data.Installations {
		if keep(st) && !slices.Contains(out, key) {
			rest = append(rest, key)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

// Last is the installation used most recently, of either kind.
func (c *Catalog) Last() (Installation, bool) {
	keys := c.Keys()
	if len(keys) == 0 {
		return Installation{}, false
	}
	return c.data.Installations[keys[0]], true
}

func (c *Catalog) lastOf(keys []string) (Installation, bool) {
	if len(keys) == 0 {
		return Installation{}, false
	}
	return c.data.Installations[keys[0]], true
}

// NewDemo returns the selections a new demo starts from: those of the last demo, without its repository,
// and with a kind cluster name that is free.
func (c *Catalog) NewDemo() Installation {
	st, _ := c.lastOf(c.Demos())
	st.Kind = KindDemo
	st.Repo = ""
	st.ClusterName = c.FreeClusterName()
	return st
}

// NewInstall returns the selections an install on the kube context starts from, and whether the context was
// installed to before. A rerun keeps everything. A new context starts from the last install on a cluster,
// without what belongs to that cluster (repository, domain, excluded apps, version). Without one, only the
// personal choices of the last demo carry over.
func (c *Catalog) NewInstall(context string) (Installation, bool) {
	if st, ok := c.Find(context); ok && st.IsCluster() {
		return st, true
	}
	if st, ok := c.lastOf(c.Clusters()); ok {
		st.Repo, st.Domain, st.UpstreamBranch, st.Context, st.ExcludedApps = "", "", "", "", nil
		return st, false
	}
	demo, _ := c.lastOf(c.Demos())
	return Installation{GitHost: demo.GitHost, Org: demo.Org, GitUserName: demo.GitUserName, UpstreamRepo: demo.UpstreamRepo}, false
}

// FreeClusterName returns the first kind cluster name of kubrix-demo, kubrix-demo-2, ... that no installation uses.
func (c *Catalog) FreeClusterName() string {
	name := DefaultClusterName
	for i := 2; ; i++ {
		if _, taken := c.data.Installations[name]; !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", DefaultClusterName, i)
	}
}

// ClusterNameFree checks that name can name a kind cluster for a demo. original is the key of the demo being
// rerun, which may keep its own name.
func (c *Catalog) ClusterNameFree(name, original string) error {
	if !clusterNameRe.MatchString(name) {
		return errors.New("allowed: lowercase letters, digits and '-'")
	}
	if _, taken := c.data.Installations[name]; taken && name != original {
		return fmt.Errorf("another saved installation already uses %q", name)
	}
	return nil
}

// Record saves the installation and makes it the most recently used one. It does not replace an installation
// of the other kind that has the same key.
func (c *Catalog) Record(st Installation) error {
	return c.write(func(f *file) error {
		key := st.Key()
		if key == "" {
			return errors.New("an installation needs a cluster name or a context")
		}
		if old, ok := f.Installations[key]; ok && old.IsCluster() != st.IsCluster() {
			return fmt.Errorf("another saved installation already uses %q", key)
		}
		if f.Installations == nil {
			f.Installations = map[string]Installation{}
		}
		f.Installations[key] = st
		f.Recent = append([]string{key}, slices.DeleteFunc(f.Recent, func(k string) bool { return k == key })...)
		return nil
	})
}

// Forget removes the installation from the catalog.
func (c *Catalog) Forget(key string) error {
	return c.write(func(f *file) error {
		delete(f.Installations, key)
		f.Recent = slices.DeleteFunc(f.Recent, func(k string) bool { return k == key })
		return nil
	})
}

// write applies fn to the file as it is on disk right now, so installations saved by another run are not lost.
func (c *Catalog) write(fn func(*file) error) error {
	if c.problem != nil {
		return fmt.Errorf("the saved installations are unreadable: %w", c.problem)
	}
	f, err := load(c.path)
	if err != nil {
		return err
	}
	if err := fn(&f); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	c.data = f
	return nil
}
