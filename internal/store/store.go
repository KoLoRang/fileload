// Package store persists FileLoad's settings as JSON files in the config
// dir. File names and shapes are compatible with v1.x so existing users
// keep their servers, mount records and commands.
package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"fileload/internal/model"
)

// Store gives typed access to every settings file. Safe for concurrent use.
type Store struct {
	servers  *jsonFile[[]model.Server]
	last     *jsonFile[model.Server]
	mounts   *jsonFile[[]model.MountRecord]
	commands *jsonFile[[]model.Command]
	ui       *jsonFile[model.UISettings]
	update   *jsonFile[model.UpdateSettings]
}

// Open returns the store rooted at dir (nothing is read until first use).
func Open(dir string) *Store {
	p := func(name string) string { return filepath.Join(dir, name) }
	return &Store{
		servers:  newJSONFile[[]model.Server](p("servers.json")),
		last:     newJSONFile[model.Server](p("last.json")),
		mounts:   newJSONFile[[]model.MountRecord](p("mounts.json")),
		commands: newJSONFile[[]model.Command](p("commands.json")),
		ui:       newJSONFile[model.UISettings](p("ui.json")),
		update:   newJSONFile[model.UpdateSettings](p("update.json")),
	}
}

var ErrNotFound = errors.New("记录不存在")

// ---- servers ----

func (s *Store) Servers() []model.Server { return slices.Clone(s.servers.get()) }

func (s *Store) Server(i int) (model.Server, error) {
	list := s.servers.get()
	if i < 0 || i >= len(list) {
		return model.Server{}, ErrNotFound
	}
	return list[i], nil
}

// UpsertServer saves p, replacing a profile for the same account.
func (s *Store) UpsertServer(p model.Server) error {
	return s.servers.update(func(list []model.Server) ([]model.Server, error) {
		list = slices.Clone(list)
		if i := slices.IndexFunc(list, p.SameEndpoint); i >= 0 {
			list[i] = p
			return list, nil
		}
		return append(list, p), nil
	})
}

func (s *Store) DeleteServer(i int) error {
	return s.servers.update(func(list []model.Server) ([]model.Server, error) {
		if i < 0 || i >= len(list) {
			return nil, ErrNotFound
		}
		return slices.Delete(slices.Clone(list), i, i+1), nil
	})
}

// LastServer is the profile used most recently (prefills the form).
func (s *Store) LastServer() (model.Server, bool) {
	p := s.last.get()
	return p, p.Host != ""
}

func (s *Store) SetLastServer(p model.Server) error { return s.last.set(p) }

// ---- mount records ----

func (s *Store) Mounts() []model.MountRecord { return slices.Clone(s.mounts.get()) }

func (s *Store) Mount(i int) (model.MountRecord, error) {
	list := s.mounts.get()
	if i < 0 || i >= len(list) {
		return model.MountRecord{}, ErrNotFound
	}
	return list[i], nil
}

func (s *Store) UpsertMount(r model.MountRecord) error {
	return s.mounts.update(func(list []model.MountRecord) ([]model.MountRecord, error) {
		list = slices.Clone(list)
		if i := slices.IndexFunc(list, r.SameMount); i >= 0 {
			list[i] = r
			return list, nil
		}
		return append(list, r), nil
	})
}

func (s *Store) DeleteMount(i int) error {
	return s.mounts.update(func(list []model.MountRecord) ([]model.MountRecord, error) {
		if i < 0 || i >= len(list) {
			return nil, ErrNotFound
		}
		return slices.Delete(slices.Clone(list), i, i+1), nil
	})
}

// ---- commands ----

// Builtins are always present and cannot be edited.
var Builtins = []model.Command{
	{Name: "rcd", Command: "", Desc: "进入挂载目录（远程 shell）", Builtin: true},
}

var cmdNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)

func isBuiltin(name string) bool {
	return slices.ContainsFunc(Builtins, func(c model.Command) bool { return strings.EqualFold(c.Name, name) })
}

// Commands returns built-ins followed by valid custom commands.
func (s *Store) Commands() []model.Command {
	out := slices.Clone(Builtins)
	for _, c := range s.commands.get() {
		if cmdNameRe.MatchString(c.Name) && !isBuiltin(c.Name) && strings.TrimSpace(c.Command) != "" {
			c.Builtin = false
			out = append(out, c)
		}
	}
	return out
}

// SaveCommand adds or updates a custom command; oldName ("" = new) is the
// command being edited, so it can be renamed.
func (s *Store) SaveCommand(oldName string, c model.Command) error {
	c.Name, c.Command, c.Desc = strings.TrimSpace(c.Name), strings.TrimSpace(c.Command), strings.TrimSpace(c.Desc)
	c.Builtin = false
	oldName = strings.TrimSpace(oldName)
	switch {
	case !cmdNameRe.MatchString(c.Name):
		return errors.New("命令名需以字母开头，只能包含字母、数字、- 和 _（最多 32 个字符）")
	case isBuiltin(c.Name):
		return fmt.Errorf("%s 是内置命令，请换一个名字", c.Name)
	case c.Command == "":
		return errors.New("请填写要在远程执行的命令")
	}
	return s.commands.update(func(list []model.Command) ([]model.Command, error) {
		list = slices.Clone(list)
		idx := -1
		for i, x := range list {
			if oldName != "" && strings.EqualFold(x.Name, oldName) {
				idx = i
			} else if strings.EqualFold(x.Name, c.Name) {
				return nil, fmt.Errorf("命令 %s 已存在", c.Name)
			}
		}
		if idx >= 0 {
			list[idx] = c
			return list, nil
		}
		return append(list, c), nil
	})
}

func (s *Store) DeleteCommand(name string) error {
	return s.commands.update(func(list []model.Command) ([]model.Command, error) {
		return slices.DeleteFunc(slices.Clone(list), func(c model.Command) bool {
			return strings.EqualFold(c.Name, name)
		}), nil
	})
}

// ---- ui / update settings ----

func (s *Store) UI() model.UISettings           { return s.ui.get() }
func (s *Store) SetUI(v model.UISettings) error { return s.ui.set(v) }
func (s *Store) UpdateToken() string            { return s.update.get().Token }
func (s *Store) SetUpdateToken(token string) error {
	return s.update.set(model.UpdateSettings{Token: strings.TrimSpace(token)})
}
