package config

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type RunConfig struct {
	Host            string
	Port            int
	UploadFolder    string
	ParallelStreams int
	DurationHours   float64

	Normal    *NormalLoad
	LargeFile *LargeFileLoad
	Download  *DownloadLoad

	NormalUsers    []UserCreds
	LargeFileUsers []UserCreds
	DownloadUsers  []UserCreds

	PollInterval   time.Duration
	TrackIDTimeout time.Duration

	// MaxConsecutiveFailures disables a user account after N consecutive
	// failed operations (uploads or downloads, counted per-user, success
	// resets the counter). The account is still recorded in the report but
	// no new operations are dispatched to it for the rest of the run.
	// 0 = never auto-disable (keep the current behaviour).
	MaxConsecutiveFailures int

	// PrivateKeyPEM, when non-empty, switches every SFTP user in the run
	// from password auth to public-key auth using THIS shared key. v1
	// model: one key for the whole run. CSV password columns are ignored
	// while the key is set. Per-user keys is a planned follow-up.
	PrivateKeyPEM string
	// PrivateKeyPassphrase decrypts PrivateKeyPEM when the key is
	// passphrase-protected. Ignored for unencrypted keys.
	PrivateKeyPassphrase string

	// Protocol picks the wire protocol the runner uses. Empty string is
	// treated as "sftp" so configs saved before v0.13.0 keep loading.
	// Valid: "sftp" | "ftp" | "ftps".
	Protocol string `json:"protocol,omitempty"`

	// TLSMode is meaningful only when Protocol == "ftps". "" / "explicit"
	// = AUTH TLS upgrade on the standard port (21); "implicit" = TLS
	// from byte 0 (canonical port 990).
	TLSMode string `json:"tls_mode,omitempty"`

	// TLSInsecureSkipVerify is the operator-supplied opt-in for self-
	// signed test servers. Disables TLS verification when true; the UI
	// gates this behind an explicit checkbox so it can never silently
	// flip on.
	TLSInsecureSkipVerify bool `json:"tls_insecure_skip_verify,omitempty"`

	// TLSServerName is the SNI value sent during the FTPS handshake.
	// Defaults to Host when empty.
	TLSServerName string `json:"tls_server_name,omitempty"`

	// TLSTrustOnFirstUse, when true, instructs the runner to add the
	// FTPS server's leaf certificate to the operator's trust store on
	// FIRST contact instead of failing the run with "unknown host".
	// Subsequent runs against the same (host, port) verify against the
	// stored fingerprint exactly like SFTP host-key TOFU. False (the
	// default) preserves the v0.13.x behaviour: unknown certs require
	// an explicit probe-and-accept through the UI before a run can use
	// them. The webui only sets this true when the operator has ticked
	// the TOFU box on the run form.
	TLSTrustOnFirstUse bool `json:"tls_trust_on_first_use,omitempty"`
}

type NormalLoad struct {
	FilesPerMinute int
	MinSizeMB      int    // inclusive, must be >= 1
	MaxSizeMB      int    // inclusive, >= MinSizeMB
	ContentType    string // "binary" (default), "ascii", or "random" (mix per file)
	// Source, when non-nil, supplies real-on-disk files instead of
	// synthetic random bytes. Per-user and per-pattern overrides let
	// the operator route specific filename patterns ("invoice*") at
	// specific input directories. Nil = default synthetic behaviour.
	Source *SourceConfig `json:"source,omitempty"`
}

type LargeFileLoad struct {
	MinSize         int    // in chosen Unit
	MaxSize         int    // in chosen Unit, >= MinSize
	Unit            string // "MB" or "GB"
	IntervalMinutes int
	// Source — same shape as NormalLoad.Source. Lets large-file uploads
	// pull from a separate pool of real files (typical for "stress with
	// real ZIP archives" scenarios).
	Source *SourceConfig `json:"source,omitempty"`
}

// DownloadLoad is an optional test phase that pulls uploaded files back down
// to measure full round-trip performance.
//
// Model: each download user independently polls its own Folder (typically
// "outbox") and downloads any file it hasn't seen before. The tool makes no
// assumption about server-side routing — whatever the server placed in a
// user's outbox is what that user pulls. This matches real platforms where
// routing is a server-side concern and the client just reads its mailbox.
type DownloadLoad struct {
	Folder          string // remote folder each download user reads from; defaults to UploadFolder if empty
	ParallelStreams int    // per download user

	// MatchMode picks how the download worker pairs an outbox file
	// back to the upload that produced it.
	//
	//   ""        — defaults to MatchModeTrackID (the historical
	//                behaviour). The upload watcher waits for the
	//                server to rename the inbox file to "<name>#<id>";
	//                downloads strip "#<id>" and look up by basename.
	//
	//   "trackid" — explicit form of the default.
	//
	//   "filename"— for SFTP servers that do NOT generate a track-id
	//                suffix. The runner injects "_slt_<12-char>_" into
	//                each upload filename and the download worker
	//                substring-matches that marker against whatever
	//                name the file ends up with in the outbox. Robust
	//                against servers that prefix or suffix the
	//                filename in transit. The track-id watcher is
	//                bypassed entirely in this mode.
	MatchMode string

	// Sink, when non-nil, persists downloaded bytes to disk instead of
	// the default io.Discard. The path template lets the operator
	// shape the on-disk layout (per-user, per-trackid, per-date) to
	// match downstream analysis pipelines. Nil = throughput-only
	// default behaviour.
	Sink *SinkConfig `json:"sink,omitempty"`
}

// SourceConfig picks where upload bytes come from. Hierarchy:
//
//   PerUser[username]      — most specific, wins outright.
//   PerPattern[patternStr] — second-most specific.
//   This struct's top level — fallback default.
//   nil                    — fall through to synthetic random bytes.
//
// Validate() rejects malformed combinations (kind=local-files with
// no Files, kind=local-dir with non-existent Dir, etc.) so the
// runner never sees a half-configured source.
type SourceConfig struct {
	// Kind is one of "synthetic", "local-files", "local-dir".
	// Empty defaults to "synthetic".
	Kind string `json:"kind,omitempty"`

	// Files is the explicit pool for kind="local-files". Each entry
	// must be a readable regular file at run-start time.
	Files []string `json:"files,omitempty"`

	// Dir is the directory to scan for kind="local-dir". Only top-level
	// regular files are picked up; subdirs and dotfiles are skipped.
	Dir string `json:"dir,omitempty"`

	// Mode selects how the pool is sampled per Next() call:
	// "round-robin" (default), "random", or "sequential" (errors on
	// pool exhaustion).
	Mode string `json:"mode,omitempty"`

	// PerPattern overrides — pattern string (e.g. "invoice*") to its
	// own SourceConfig. Looked up after PerUser, before this struct's
	// top-level fields.
	PerPattern map[string]*SourceConfig `json:"per_pattern,omitempty"`

	// PerUser overrides — username to its own SourceConfig. Most
	// specific, wins outright when matched.
	PerUser map[string]*SourceConfig `json:"per_user,omitempty"`
}

// SinkConfig picks where downloaded bytes go.
//
//   Kind="discard"    — default, writes go to io.Discard.
//   Kind="local-disk" — writes to <Root>/<rendered Template>.
//
// Template variables: {user}, {filename}, {basename}, {ext}, {trackid},
// {run_id}, {date}, {datetime}. Path-component sanitisation prevents
// any rendered value containing "/" or ".." from escaping <Root>.
type SinkConfig struct {
	Kind      string `json:"kind,omitempty"`      // "discard" | "local-disk" — default "discard"
	Root      string `json:"root,omitempty"`      // base dir for local-disk; auto-mkdir
	Template  string `json:"template,omitempty"`  // path template; default "{user}/{filename}"
	Overwrite bool   `json:"overwrite,omitempty"` // false = O_EXCL, error if file exists
}

// Validate enforces structural invariants on a SourceConfig at run-start.
// Walks per-user / per-pattern overrides too so a deeply-nested mistake
// is caught before the runner spawns its first goroutine. The label is
// prepended to error messages ("normal" / "large-file" / etc.) so a
// failure points at the right config section.
//
// Nil receiver is fine — returns nil. The runner will fall through to
// the synthetic default.
func (s *SourceConfig) Validate(label string) error {
	if s == nil {
		return nil
	}
	kind := s.Kind
	if kind == "" {
		kind = "synthetic"
	}
	switch kind {
	case "synthetic":
		// no further fields to validate
	case "local-files":
		if len(s.Files) == 0 {
			return errors.New(label + " source kind=local-files requires non-empty files[]")
		}
	case "local-dir":
		if s.Dir == "" {
			return errors.New(label + " source kind=local-dir requires dir")
		}
	default:
		return errors.New(label + ` source kind must be "synthetic", "local-files", or "local-dir"`)
	}
	switch s.Mode {
	case "", "round-robin", "random", "sequential":
		// ok
	default:
		return errors.New(label + ` source mode must be "round-robin", "random", or "sequential"`)
	}
	for pat, sub := range s.PerPattern {
		if err := sub.Validate(label + ".per_pattern[" + pat + "]"); err != nil {
			return err
		}
	}
	for u, sub := range s.PerUser {
		if err := sub.Validate(label + ".per_user[" + u + "]"); err != nil {
			return err
		}
	}
	return nil
}

// Validate enforces structural invariants on a SinkConfig.
// Nil receiver returns nil — the runner falls through to discard.
func (s *SinkConfig) Validate() error {
	if s == nil {
		return nil
	}
	kind := s.Kind
	if kind == "" {
		kind = "discard"
	}
	switch kind {
	case "discard":
		// nothing to check
	case "local-disk":
		if s.Root == "" {
			return errors.New(`download sink kind=local-disk requires root`)
		}
	default:
		return errors.New(`download sink kind must be "discard" or "local-disk"`)
	}
	return nil
}

// MatchMode constants for DownloadLoad.MatchMode.
const (
	MatchModeTrackID  = "trackid"
	MatchModeFilename = "filename"
)

type UserCreds struct {
	Username string
	Password string
	Patterns []string
}

// String implements fmt.Stringer with the password masked so that a stray
// `%v` or `%+v` on a UserCreds (or a slice of them) never leaks credentials
// into a log line. The actual password is still readable via .Password for
// the SSH client; this only changes default formatting.
func (u UserCreds) String() string {
	mask := ""
	if u.Password != "" {
		mask = "***"
	}
	return fmt.Sprintf("{User:%s Password:%s Patterns:%v}", u.Username, mask, u.Patterns)
}

// GoString likewise masks the password so `%#v` formatting (used by some
// debuggers and structured loggers) is also safe.
func (u UserCreds) GoString() string { return u.String() }

func ParseUsersCSV(r io.Reader) ([]UserCreds, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	var users []UserCreds
	lineNo := 0
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		lineNo++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if len(rec) < 3 {
			return nil, fmt.Errorf("line %d: need at least username,password,pattern*", lineNo)
		}
		u := UserCreds{
			Username: strings.TrimSpace(rec[0]),
			Password: rec[1],
		}
		for _, p := range rec[2:] {
			p = strings.TrimSpace(p)
			if p != "" {
				u.Patterns = append(u.Patterns, p)
			}
		}
		if u.Username == "" || len(u.Patterns) == 0 {
			return nil, fmt.Errorf("line %d: username and at least one pattern required", lineNo)
		}
		users = append(users, u)
	}
	return users, nil
}

func (c *RunConfig) Validate() error {
	if c.Host == "" {
		return errors.New("host is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("port must be 1-65535")
	}
	if c.UploadFolder == "" {
		return errors.New("upload folder is required")
	}
	if c.ParallelStreams < 1 {
		c.ParallelStreams = 1
	}
	if c.DurationHours <= 0 {
		return errors.New("duration must be > 0")
	}
	if c.Normal == nil && c.LargeFile == nil {
		return errors.New("enable at least one of normal-load or large-file-load")
	}
	if c.Normal != nil {
		if len(c.NormalUsers) == 0 {
			return errors.New("normal-load needs at least one user")
		}
		if c.Normal.FilesPerMinute <= 0 {
			return errors.New("files_per_minute must be > 0")
		}
		if c.Normal.MinSizeMB < 1 {
			return errors.New("normal min_size_mb must be >= 1")
		}
		if c.Normal.MaxSizeMB < c.Normal.MinSizeMB {
			c.Normal.MaxSizeMB = c.Normal.MinSizeMB
		}
		switch c.Normal.ContentType {
		case "", "binary", "ascii", "random":
			if c.Normal.ContentType == "" {
				c.Normal.ContentType = "binary"
			}
		default:
			return errors.New(`normal content_type must be "binary", "ascii", or "random"`)
		}
		if err := c.Normal.Source.Validate("normal"); err != nil {
			return err
		}
	}
	if c.LargeFile != nil {
		if len(c.LargeFileUsers) == 0 {
			return errors.New("large-file-load needs at least one user")
		}
		if c.LargeFile.Unit != "MB" && c.LargeFile.Unit != "GB" {
			return errors.New("large-file unit must be \"MB\" or \"GB\"")
		}
		if c.LargeFile.MinSize < 1 {
			return errors.New("large-file min size must be >= 1")
		}
		if c.LargeFile.MaxSize < c.LargeFile.MinSize {
			c.LargeFile.MaxSize = c.LargeFile.MinSize
		}
		if c.LargeFile.IntervalMinutes <= 0 {
			return errors.New("interval_minutes must be > 0")
		}
		if err := c.LargeFile.Source.Validate("large-file"); err != nil {
			return err
		}
	}
	if c.Download != nil {
		if len(c.DownloadUsers) == 0 {
			return errors.New("download test needs at least one user")
		}
		if c.Download.ParallelStreams < 1 {
			c.Download.ParallelStreams = 1
		}
		if c.Download.Folder == "" {
			c.Download.Folder = c.UploadFolder
		}
		if err := c.Download.Sink.Validate(); err != nil {
			return err
		}
	}
	if c.PollInterval == 0 {
		c.PollInterval = 3 * time.Second
	}
	if c.MaxConsecutiveFailures < 0 {
		c.MaxConsecutiveFailures = 0
	}
	if c.TrackIDTimeout == 0 {
		c.TrackIDTimeout = 5 * time.Minute
	}
	return nil
}
