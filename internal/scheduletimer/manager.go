package scheduletimer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const timerUnit = "swarm-schedule.timer"

type manager struct {
	environment []string
	platform    string
	paths       []string
	run         func(string, ...string) (string, error)
}

func command(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), err
}

type state struct {
	active   bool
	enabled  string
	path     string
	activity string
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (m manager) inspect() (state, error) {
	if m.platform == "darwin" {
		out, err := m.run("launchctl", "print", domain()+"/"+label)
		if err != nil {
			if strings.Contains(out, "Could not find service \""+label+"\" in domain") {
				return state{activity: "inactive"}, nil
			}
			return state{}, err
		}
		// A calendar job waiting between ticks is armed even without a live PID.
		for _, line := range strings.Split(out, "\n") {
			if p, ok := strings.CutPrefix(strings.TrimSpace(line), "path = "); ok {
				return state{active: true, activity: "active", path: p}, nil
			}
		}
		return state{}, errors.New("loaded launchd job has no definition path")
	}
	out, err := m.run("systemctl", "--user", "show", "--property=LoadState,ActiveState,UnitFileState,FragmentPath", timerUnit)
	var s state
	var load string
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "LoadState":
			load = v
		case "ActiveState":
			s.activity = v
		case "UnitFileState":
			s.enabled = v
		case "FragmentPath":
			s.path = v
		}
	}
	if load == "not-found" && s.activity == "inactive" && s.path == "" && (err == nil || exitCode(err) == 4) {
		return state{activity: "inactive"}, nil
	}
	if err != nil {
		return state{}, err
	}
	if load != "loaded" || s.activity == "" || s.enabled == "" || s.path == "" {
		return state{}, fmt.Errorf("unsupported systemd status: %q", out)
	}
	s.active = s.activity == "active"
	return s, nil
}

func exitCode(err error) int {
	var e *exec.ExitError
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return -1
}

func (m manager) status() (string, error) {
	count := 0
	for _, p := range m.paths {
		st, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("expected regular unit file at %s", p)
		}
		count++
	}
	installed := "partially installed"
	switch count {
	case 0:
		installed = "not installed"
	case len(m.paths):
		installed = "installed"
	}
	s, err := m.inspect()
	if err != nil {
		return "", fmt.Errorf("%s; runtime status unknown: %w", installed, err)
	}
	return installed + ", " + s.activity, nil
}

func (m manager) ctl(args ...string) error {
	var err error
	if m.platform == "darwin" {
		_, err = m.run("launchctl", args...)
	} else {
		_, err = m.run("systemctl", append([]string{"--user"}, args...)...)
	}
	return err
}

func (m manager) setActive(active bool) error {
	if m.platform == "darwin" {
		if active {
			return m.ctl("bootstrap", domain(), m.paths[0])
		}
		return m.ctl("bootout", domain()+"/"+label)
	}
	if active {
		return m.ctl("start", timerUnit)
	}
	return m.ctl("stop", timerUnit)
}

func activity(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}

func (m manager) toggle(t *transaction, wasActive bool) error {
	// A command can fail after changing manager state.
	t.remember(func() error {
		current, err := m.inspect()
		if err != nil || current.activity == activity(wasActive) {
			return err
		}
		return m.setActive(wasActive)
	})
	return m.setActive(!wasActive)
}

func (m manager) enable(t *transaction, enable bool) error {
	set := func(on bool) error {
		if on {
			return m.ctl("enable", timerUnit)
		}
		return m.ctl("disable", timerUnit)
	}
	t.remember(func() error {
		s, err := m.inspect()
		if err != nil || (s.enabled == "enabled") != enable {
			return err
		}
		return set(!enable)
	})
	return set(enable)
}

func (m manager) change(exe string) (err error) {
	install := exe != ""
	dir := filepath.Dir(m.paths[0])
	if install {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	d, err := os.Open(dir)
	if err == nil {
		defer func() { _ = d.Close() }()
		if err = syscall.Flock(int(d.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var old []definition
	for _, p := range m.paths {
		f, e := snapshot(p)
		if e != nil {
			return e
		}
		old = append(old, f)
	}
	s, err := m.inspect()
	if err != nil {
		return err
	}
	last := old[len(old)-1]
	if s.path != "" && filepath.Clean(s.path) != last.path {
		a, e1 := os.Stat(s.path)
		b, e2 := os.Stat(last.path)
		if e1 != nil || e2 != nil || !os.SameFile(a, b) {
			return fmt.Errorf("timer manager uses another definition at %s", s.path)
		}
	}
	if s.active && (!last.exists || s.path == "") {
		return errors.New("loaded timer has no restorable definition")
	}
	if s.activity != "active" && s.activity != "inactive" {
		return fmt.Errorf("cannot preserve timer state %q", s.activity)
	}
	switch s.enabled {
	case "", "enabled", "disabled":
	default:
		return fmt.Errorf("cannot preserve unit file state %q", s.enabled)
	}
	linux := m.platform == "linux"
	var rendered []string
	if install {
		if linux {
			rendered = []string{renderSystemdService(exe, m.environment...), renderSystemdTimer()}
		} else {
			rendered = []string{renderLaunchd(exe, m.environment...)}
		}
	}
	type edit struct{ before, after definition }
	var edits []edit
	for i, f := range old {
		next := definition{path: f.path, exists: install}
		if install {
			next.mode = 0600
			next.data = []byte(rendered[i])
		}
		if f.exists != next.exists || f.mode != next.mode || !bytes.Equal(f.data, next.data) {
			edits = append(edits, edit{f, next})
		}
	}
	var tx transaction
	defer func() { tx.finish(&err) }()

	if s.active && (!install || len(edits) > 0) {
		if err = m.toggle(&tx, true); err != nil {
			return err
		}
	}
	if !install && linux && s.enabled == "enabled" {
		if err = m.enable(&tx, false); err != nil {
			return err
		}
	}

	reload := linux && (len(edits) > 0 || install && s.path == "")
	if reload {
		tx.remember(func() error { return m.ctl("daemon-reload") })
	}
	for _, edit := range edits {
		if err = edit.after.apply(); err != nil {
			return err
		}
		tx.remember(edit.before.apply)
	}
	if reload {
		if err = m.ctl("daemon-reload"); err != nil {
			return err
		}
	}
	if install && linux && s.enabled != "enabled" {
		if err = m.enable(&tx, true); err != nil {
			return err
		}
	}
	if install && (!s.active || len(edits) > 0) {
		return m.toggle(&tx, false)
	}
	return nil
}
