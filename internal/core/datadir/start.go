package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// FilePerm is the permission of the location file.
const FilePerm fs.FileMode = 0o600

// Startup is what Start found and did.
type Startup struct {
	Resolution
	// Moved is the folder a move scheduled by the previous run copied
	// from, when it happened in this Start.
	Moved *Previous
}

// Start resolves the directories, carries out a scheduled data directory
// change and creates what is missing. Directories that cannot be used are
// left empty in Dirs, with a problem saying why:
//
//   - In ModeEnv and ModePortable a root that cannot be written (a
//     read-only disk) empties every directory, so nothing is written
//     elsewhere.
//   - A chosen data directory that does not exist empties Data. It is not
//     created, since it may be on a disk that is not mounted.
//
// The error is for what makes the app unable to start at all: no home
// directory, or default directories that cannot be created.
func Start(env Env, now time.Time) (Startup, error) {
	r, err := Resolve(env)
	if err != nil {
		return Startup{}, err
	}
	st := Startup{Resolution: r}

	switch r.Mode {
	case ModeEnv, ModePortable:
		if err := ensureWritable(r.Dirs); err != nil {
			where := "POSTSONDE_HOME (" + r.Root + ")"
			if r.Mode == ModePortable {
				where = "the portable data folder " + r.Root
			}
			st.Problems = append(st.Problems, errs.Wrap(err, errs.KindOf(err), "PostSonde cannot write to "+where+", so nothing is saved in this session.").
				WithCode(CodeUnwritable).
				WithHint("Portable mode needs a writable folder: copy the app and its postsonde.portable file to a USB stick or folder you can write to (not a mounted disk image or a system folder).").
				WithDetail("path", r.Root))
			st.Dirs = appdirs.Dirs{}
		}
		return st, nil
	}

	// Default and configured: settings, cache and logs are the OS
	// locations and must work.
	base := st.Dirs
	base.Data = base.Config // Ensure needs a value; Data is handled below
	if err := base.Ensure(); err != nil {
		return Startup{}, err
	}
	if r.Location.Pending != nil {
		st.runPending(now)
	}
	st.ensureData()
	return st, nil
}

// ensureData creates the default data directory, or checks that the chosen
// one exists.
func (st *Startup) ensureData() {
	dir := st.Dirs.Data
	if dir == "" {
		return
	}
	if st.Mode == ModeDefault {
		if err := os.MkdirAll(dir, appdirs.DirPerm); err != nil {
			st.Problems = append(st.Problems, errs.Wrap(err, errs.KindOf(err), "The data directory could not be created, so nothing is saved in this session.").
				WithCode(CodeUnwritable).WithDetail("path", dir))
			st.Dirs.Data = ""
		}
		return
	}
	fi, err := os.Stat(dir)
	switch {
	case err == nil && fi.IsDir():
		return
	case err == nil:
		err = fmt.Errorf("%s is not a folder", dir)
	}
	st.Problems = append(st.Problems, errs.Wrap(err, errs.NotFound, "The data directory "+dir+" is not available, so projects cannot be opened.").
		WithCode(CodeNotFound).
		WithHint("If it is on a disk that is not connected, connect it and restart PostSonde. Otherwise choose another data directory in Settings, General.").
		WithDetail("path", dir))
	st.Dirs.Data = ""
}

// runPending carries out the scheduled change and records the outcome in
// the location file. On failure the data directory stays as it was.
func (st *Startup) runPending(now time.Time) {
	loc := st.Location
	p := *loc.Pending
	loc.Pending = nil
	fail := func(err error, msg, hint string) {
		st.Problems = append(st.Problems, errs.Wrap(err, errs.KindOf(err), msg).
			WithCode(CodeMoveFailed).WithHint(hint).WithDetail("path", p.DataDir))
		if werr := writeLocation(st.LocationPath, loc); werr != nil {
			st.Problems = append(st.Problems, errs.Wrap(werr, errs.KindOf(werr), "The data directory setting could not be saved.").
				WithCode(CodeUnwritable).WithDetail("path", st.LocationPath))
		}
		st.Location = loc
	}
	keep := "Your data is still in " + st.Dirs.Data + "; nothing was changed."
	if !samePath(p.From, st.Dirs.Data) {
		fail(errors.New("the data directory changed after the change was scheduled"),
			"The scheduled data directory change was dropped.", "Choose the new folder again in Settings, General.")
		return
	}
	if samePath(p.DataDir, st.Dirs.Data) {
		fail(errors.New("the folder is already the data directory"), "The scheduled data directory change was dropped.", "")
		return
	}

	var moved *Previous
	switch p.Action {
	case ActionMove:
		entries, err := CopyData(st.Dirs.Data, p.DataDir)
		if err != nil {
			fail(err, "Moving the data directory to "+p.DataDir+" failed.", keep)
			return
		}
		moved = &Previous{DataDir: st.Dirs.Data, Entries: entries, MovedAt: now.UTC().Format(time.RFC3339)}
	case ActionFresh:
		if err := os.MkdirAll(p.DataDir, appdirs.DirPerm); err != nil {
			fail(err, "The new data directory "+p.DataDir+" could not be created.", keep)
			return
		}
		if hasData(p.DataDir) {
			fail(errors.New("the folder now holds PostSonde data"), "The new data directory "+p.DataDir+" is no longer empty.", keep)
			return
		}
	case ActionUse:
		if !hasData(p.DataDir) {
			fail(errors.New("no PostSonde data found"), "The folder "+p.DataDir+" no longer holds PostSonde data.", keep)
			return
		}
	}

	loc.DataDir = p.DataDir
	if samePath(p.DataDir, st.Default) {
		loc.DataDir = ""
	}
	if moved != nil {
		loc.Previous = moved
	}
	if err := writeLocation(st.LocationPath, loc); err != nil {
		if moved != nil {
			// Undo the copy so the next attempt starts clean.
			_ = removeEntries(p.DataDir, moved.Entries)
		}
		st.Problems = append(st.Problems, errs.Wrap(err, errs.KindOf(err), "The data directory setting could not be saved, so the change was not made.").
			WithCode(CodeMoveFailed).WithHint(keep).WithDetail("path", st.LocationPath))
		return
	}
	st.Location = loc
	st.Moved = moved
	st.Dirs.Data = filepath.Clean(p.DataDir)
	st.Mode = ModeConfigured
	if loc.DataDir == "" {
		st.Mode = ModeDefault
	}
}

// writeLocation saves loc atomically, creating the folder if needed.
func writeLocation(path string, loc Location) error {
	loc.Version = LocationVersion
	if err := os.MkdirAll(filepath.Dir(path), appdirs.DirPerm); err != nil {
		return err
	}
	return atomicfile.WriteJSON(path, loc, FilePerm)
}

// ensureWritable creates the directories of d and checks that a file can
// be written in each (MkdirAll succeeds on a read-only disk when they
// exist already).
func ensureWritable(d appdirs.Dirs) error {
	if err := d.Ensure(); err != nil {
		return err
	}
	for _, dir := range []string{d.Config, d.Data, d.Cache, d.Logs} {
		if err := probeWrite(dir); err != nil {
			return err
		}
	}
	return nil
}

// probeWrite writes and removes a temporary file in dir.
func probeWrite(dir string) error {
	f, err := os.CreateTemp(dir, ".postsonde-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
