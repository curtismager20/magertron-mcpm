package main

// mcpctl submit — hand a server you have built to the people who govern it.
//
// ⚠ SUBMITTING IS NOT DEPLOYING, and the distinction is the whole reason this
// command is safe to put in a developer's hands. It posts an OBSERVATION: a
// statement that a server exists. Correlation rolls it into inventory_servers,
// it appears in the review queue, and the AI platform team decides whether and
// when it is deployed. Nothing becomes routable because a developer ran a CLI.
//
// A push-to-deploy button would invert the product. The platform's premise is
// that an administrator holds the gate; a tool that lets any developer place
// something on the far side of it is the shape Magertron sells against.
//
// WHAT IT IS FOR. Today an administrator DISCOVERS servers by probing for
// them. This inverts that: a developer volunteers what they built, because
// submitting is easier than being found. The credential carries the
// attribution — an admin issues the token to a team, so a submission arrives
// already saying WHO is asking.
//
// REVISIONS WORK. The correlation cascade in mcp-inventory matches on
// endpoint_url, then (package_ecosystem, package_name), then name+transport.
// Submitting v2 of a server lands on v1's row rather than beside it — so a
// second submission of an already-deployed server reads as declared drift,
// with a known cause, rather than "something changed under us".
//
// ⚠ STDIO IS ACCEPTED HERE, and deliberately. The deploy wizard's importer
// REJECTS stdio as policy — Magertron will not run one. But inventory records
// what EXISTS, and most of what exists on developer machines is stdio.
// Recording that the payments team built a stdio server is useful intelligence
// even though it is undeployable; refusing to record it would throw away the
// visibility this feature is for. The CLI warns; the platform's policies stop
// it going further.

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ─── wire types — mirror services/mcp-inventory/internal/ingest/types.go ────
//
// ⚠ Kept structurally identical to the Go on the other side. If that contract
// changes, this must change with it — a submission the server rejects is worse
// than no submission, because the developer believes they are covered.

type obsAgent struct {
	AgentID string `json:"agent_id"`

	// Which tool submitted this — "mcpctl", "vscode", later "cursor". From
	// MCPCTL_CLIENT, so a bundled binary identifies its host without a flag.
	AgentVersion string `json:"agent_version"`

	HostID string `json:"host_id"`
	HostOS string `json:"host_os,omitempty"`

	// ⚠ A CLAIM, NOT AN IDENTITY. The OS username of whoever ran the command.
	// Nothing verifies it and anything could set it. It rides along because
	// "ask Sarah" beats "ask the payments team" when a submission needs a
	// conversation — and it is worth nothing as evidence.
	//
	// ⚠ The orchestrator OVERWRITES user_id with the authenticated subject
	// before storing. This travels separately so the unverifiable claim and
	// the stamped identity are never mistaken for one another.
	UserID string `json:"user_id,omitempty"`

	CollectedAt time.Time `json:"collected_at"`
}

type obsPackage struct {
	Ecosystem   string `json:"ecosystem"`
	Name        string `json:"name"`
	VersionSpec string `json:"version_spec,omitempty"`
}

type obsServer struct {
	DeclaredName string         `json:"declared_name"`
	Transport    string         `json:"transport"`
	Command      string         `json:"command,omitempty"`
	Args         []string       `json:"args,omitempty"`
	EnvKeys      []string       `json:"env_keys,omitempty"`
	EndpointURL  string         `json:"endpoint_url,omitempty"`
	Package      *obsPackage    `json:"package,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"`
}

type observation struct {
	ObservationID string    `json:"observation_id"`
	SourceKind    string    `json:"source_kind"`
	SourcePath    string    `json:"source_path,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
	Server        obsServer `json:"server"`

	// ⚠ THE ORIGINAL MANIFEST, scrubbed. The flattened Server fields above are
	// enough to LIST a server; they are nowhere near enough to ONBOARD one.
	// Carrying the manifest means the deploy wizard can be prefilled from what
	// the developer actually wrote — and by the classifier that already
	// exists, rather than a second one built against five columns.
	Raw map[string]any `json:"raw,omitempty"`
}

type ingestRequest struct {
	Agent        obsAgent      `json:"agent"`
	Observations []observation `json:"observations"`
}

type rejectedItem struct {
	ObservationID string `json:"observation_id"`
	Reason        string `json:"reason"`
}

type ingestResponse struct {
	AcceptedCount int            `json:"accepted_count"`
	Rejected      []rejectedItem `json:"rejected"`
	RequestID     string         `json:"request_id"`
}

// ─── manifest shapes — MCP Registry server.json, schema 2025-12-11 ──────────
//
// ⚠ Field casing is camelCase in the spec (registryType, registryBaseUrl).
// Older documents use snake_case; both are tolerated on read, matching what
// ui/src/components/mcpServerJsonImporter.ts does. Do NOT "fix" one to the
// other — a manifest in the wild may use either.

type mfTransport struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type mfEnvVar struct {
	Name string `json:"name"`
}

type mfRemote struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type mfPackage struct {
	RegistryType    string      `json:"registryType"`
	RegistryTypeAlt string      `json:"registry_type"`
	Identifier      string      `json:"identifier"`
	Name            string      `json:"name"`
	Version         string      `json:"version"`
	Transport       mfTransport `json:"transport"`
	EnvironmentVars []mfEnvVar  `json:"environmentVariables"`
	EnvVarsAlt      []mfEnvVar  `json:"environment_variables"`
	RuntimeArgs     []struct {
		Value string `json:"value"`
	} `json:"runtimeArguments"`
}

type manifest struct {
	Name        string      `json:"name"`
	Version     string      `json:"version"`
	Description string      `json:"description"`
	Remotes     []mfRemote  `json:"remotes"`
	Packages    []mfPackage `json:"packages"`
}

// ─── reading the bundle ─────────────────────────────────────────────────────

// readManifest returns the manifest JSON from a .mcpb (a ZIP containing
// manifest.json) or from a plain server.json / *.json file.
//
// Mirrors ui/src/components/ManifestDropZone.tsx, which does the same two
// things in the browser. One format, two readers — keep them agreeing.
func readManifest(path string) ([]byte, error) {
	lower := strings.ToLower(path)

	if strings.HasSuffix(lower, ".mcpb") || strings.HasSuffix(lower, ".zip") {
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("could not open %s as a bundle: %w", filepath.Base(path), err)
		}
		defer zr.Close()

		for _, f := range zr.File {
			// Accept manifest.json at the root or one directory down — bundles
			// are built by several tools and they do not agree on layout.
			base := strings.ToLower(filepath.Base(f.Name))
			if base != "manifest.json" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("could not read manifest.json inside the bundle: %w", err)
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
		return nil, fmt.Errorf("no manifest.json found inside %s", filepath.Base(path))
	}

	return os.ReadFile(path)
}

// ─── classification ─────────────────────────────────────────────────────────

// classify maps a manifest onto what the inventory contract needs:
// a declared_name, a transport, and enough to identify the thing again later.
//
// ⚠ This is NOT the deploy wizard's classifier and must not be confused with
// it. mcpServerJsonImporter.ts answers "can Magertron RUN this?" and rejects
// what it cannot. This answers "what IS this?" and records it either way.
// A submission that cannot be deployed is still worth knowing about.
//
// Returns the observed server, plus advisory notes for the developer.
func classify(m *manifest) (obsServer, []string, error) {
	var notes []string

	name := strings.TrimSpace(m.Name)
	if name == "" {
		return obsServer{}, nil, fmt.Errorf("manifest has no name — cannot submit an unnamed server")
	}

	// ── 1. a remote endpoint. Correlates on endpoint_url, the strongest key.
	for _, r := range m.Remotes {
		if u := strings.TrimSpace(r.URL); u != "" {
			t := normalizeTransport(r.Type)
			if t == "" {
				t = "http"
			}
			return obsServer{
				DeclaredName: name,
				Transport:    t,
				EndpointURL:  u,
			}, notes, nil
		}
	}

	// ── 2. a package. Correlates on (ecosystem, name) — stable across
	//       versions, which is what makes a v2 submission update v1's row.
	for _, p := range m.Packages {
		reg := firstNonEmpty(p.RegistryType, p.RegistryTypeAlt)
		ident := firstNonEmpty(p.Identifier, p.Name)
		if ident == "" {
			continue
		}

		transport := normalizeTransport(p.Transport.Type)
		if transport == "" {
			transport = "stdio"
		}

		srv := obsServer{
			DeclaredName: name,
			Transport:    transport,
			EndpointURL:  strings.TrimSpace(p.Transport.URL),
			Package: &obsPackage{
				Ecosystem:   strings.ToLower(reg),
				Name:        ident,
				VersionSpec: p.Version,
			},
			EnvKeys: envKeyNames(p),
		}

		for _, a := range p.RuntimeArgs {
			if v := strings.TrimSpace(a.Value); v != "" {
				srv.Args = append(srv.Args, v)
			}
		}

		// ⚠ A remote transport with no URL cannot be reached, and the
		// validator will reject it. Say so here rather than letting the
		// developer discover it in a rejected[] entry.
		if (transport == "http" || transport == "sse") && srv.EndpointURL == "" {
			return obsServer{}, nil, fmt.Errorf(
				"package declares %s transport but no url — nothing could reach this server",
				transport)
		}

		if transport == "stdio" {
			notes = append(notes,
				"transport is stdio: recorded for visibility, but Magertron does "+
					"not deploy stdio servers. To make it deployable, expose it "+
					"over streamable-http or sse.")
			// The validator needs command OR package for stdio; we have the
			// package, so this passes.
		}

		if strings.ToLower(reg) != "oci" && transport != "http" && transport != "sse" {
			notes = append(notes,
				fmt.Sprintf("registry type %q is not an OCI image: Magertron has "+
					"nothing to run as a pod. Recorded, not deployable.", reg))
		}

		return srv, notes, nil
	}

	return obsServer{}, nil, fmt.Errorf(
		"manifest describes neither a remote endpoint nor a package — " +
			"nothing to record. A docs-only server.json cannot be submitted.")
}

// normalizeTransport maps the registry spelling onto the inventory contract's
// closed enum: stdio | http | sse. Anything unrecognised returns "" so the
// caller can default deliberately rather than shipping a value the validator
// will reject.
func normalizeTransport(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "stdio":
		return "stdio"
	case "sse":
		return "sse"
	case "http", "streamable-http", "streamable_http", "streamablehttp":
		return "http"
	default:
		return ""
	}
}

// envKeyNames returns environment variable NAMES only.
//
// ⚠ NEVER ship values. The inventory contract says so explicitly and it is the
// reason a developer can be asked to run this at all: a manifest may carry
// defaults that are secrets, and an inventory system that hoovered them up
// would be a liability rather than a control.
func envKeyNames(p mfPackage) []string {
	var out []string
	for _, e := range append(p.EnvironmentVars, p.EnvVarsAlt...) {
		if n := strings.TrimSpace(e.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// scrubbed parses the manifest for transport to inventory, with anything that
// could be a credential removed.
//
// ⚠ A MANIFEST CAN CARRY SECRETS. The MCP registry schema gives environment
// variables an optional `default`, and nothing stops a developer putting a
// real key there — in a file they are about to hand to a platform team, from a
// CLI they were told was safe to run. The ingest contract already says env
// KEYS only; this makes the raw passthrough honour the same rule rather than
// quietly reintroducing what the flattened fields were careful to exclude.
//
// Conservative by construction: it strips `default` and `value` from anything
// under an environmentVariables / environment_variables / runtimeArguments
// list, and drops any top-level key that looks like a credential. A manifest
// field this does not recognise survives — the alternative, an allowlist,
// would silently discard whatever the spec adds next.
func scrubbed(rawJSON []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(rawJSON, &m); err != nil {
		return nil
	}
	scrubValue(m)
	return m
}

// alwaysSecret — key names that mean a credential wherever they appear. No
// honest manifest field is called "password" and means something else.
var alwaysSecret = map[string]bool{
	"password": true, "secret": true, "token": true,
	"apikey": true, "api_key": true, "credential": true,
	"credentials": true, "authorization": true, "client_secret": true,
	"clientsecret": true, "private_key": true, "privatekey": true,
}

// contextualSecret — key names that mean a credential ONLY in a credential
// position.
//
// ⚠ THIS DISTINCTION EXISTS BECAUSE THE BLANKET RULE ATE ARGUMENTS. `value`
// under environmentVariables may be a secret; `value` under runtimeArguments is
// a command-line flag. Stripping both turned
//
//	"runtimeArguments": [ { "value": "--serve" } ]
//
// into
//
//	"runtimeArguments": [ {} ]
//
// destroying the container's arguments — which the deploy wizard prefills from,
// so the operator would have onboarded a server that starts wrong, with nothing
// on screen to say why.
//
// ⚠ Over-scrubbing is a failure, not caution. It degrades the artifact the
// operator is meant to review, and the degradation is invisible.
var contextualSecret = map[string]bool{
	"default": true, "value": true,
}

// credentialContext — is this key a container of credential material?
//
// Matched loosely on purpose: "environmentVariables", "environment_variables"
// and "envVars" all appear in manifests found in the wild, and a spelling this
// does not recognise fails OPEN (values survive) rather than closed. That is
// the right direction for a list of NAMES; the always-secret set above is what
// catches an actual key regardless of where it hides.
func credentialContext(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "environmentvariable") ||
		strings.Contains(k, "environment_variable") ||
		strings.Contains(k, "envvar") ||
		strings.Contains(k, "env_var")
}

func scrubValue(v any) { scrubIn(v, false) }

// scrubIn walks the document, carrying whether we are inside a credential
// position. inCred is inherited: an object nested under environmentVariables
// is still in a credential position.
func scrubIn(v any, inCred bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lower := strings.ToLower(k)

			if alwaysSecret[lower] {
				// ⚠ Delete rather than blank. A key present with an empty value
				// reads as "the developer set nothing" — a different and
				// misleading claim about what was submitted.
				delete(t, k)
				continue
			}
			if inCred && contextualSecret[lower] {
				delete(t, k)
				continue
			}
			scrubIn(child, inCred || credentialContext(k))
		}
	case []any:
		for _, child := range t {
			scrubIn(child, inCred)
		}
	}
}

// clientName reports which tool is submitting.
//
// ⚠ From MCPCTL_CLIENT so a BUNDLED binary can identify its host without the
// host passing a flag. An IDE extension sets it once when it builds the child
// environment; nothing about the command line changes.
//
// Defaults to "mcpctl" — a bare terminal invocation, which is the truth when
// nothing claims otherwise.
func clientName() string {
	if v := strings.TrimSpace(os.Getenv("MCPCTL_CLIENT")); v != "" {
		// ⚠ Bounded. This reaches a database column and a screen, and an
		// unbounded client-supplied string in either is somebody else's
		// incident.
		if len(v) > 32 {
			v = v[:32]
		}
		return v
	}
	return "mcpctl"
}

// claimedUser reports who the OS says is running this.
//
// ⚠ UNVERIFIED, AND CALLERS MUST TREAT IT SO. os/user reads the process owner,
// which is trivially not the person — a shared CI account, a container running
// as root, anyone who exported USER before invoking. It is a starting point
// for a conversation, not an identity.
//
// Empty rather than a guess when it cannot be determined: "unknown" would read
// like a value.
func claimedUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// ─── ids ────────────────────────────────────────────────────────────────────

// newObservationID returns a lexicographically-sortable id.
//
// ⚠ ULID-SHAPED, not a strict ULID — the contract calls for a ULID and this is
// a timestamp prefix plus randomness, which sorts correctly and does not
// collide. If a real ULID matters (it does not today; nothing parses it),
// swap in a library rather than making this cleverer.
func newObservationID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%013x%s", time.Now().UTC().UnixMilli(), hex.EncodeToString(b[:]))
}

// ─── the command ────────────────────────────────────────────────────────────

type submitOpts struct {
	Path    string // .mcpb or server.json
	AgentID string // informational only — see below
	UserID  string
	DryRun  bool
}

// ⚠ NO TOKEN OR URL HERE. Both come from the stored config via apiRequest,
// which also carries the TLS settings and — importantly — the leader-bounce
// retry. A hand-rolled http.Client would have to reimplement all three, and
// would get the retry wrong, which is the failure that looks like the platform
// being flaky.
//
// ⚠ AgentID is INFORMATIONAL. The orchestrator OVERWRITES agent_id and user_id
// from the authenticated principal before forwarding, so whatever is set here
// does not decide attribution. It is sent anyway because a dry-run should show
// the true shape of the payload, and because the field is required by the
// ingest contract.

func runSubmit(gf globalFlags, o submitOpts) error {
	raw, err := readManifest(o.Path)
	if err != nil {
		return err
	}

	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("manifest is not valid JSON: %w", err)
	}

	srv, notes, err := classify(&m)
	if err != nil {
		return err
	}

	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}

	req := ingestRequest{
		Agent: obsAgent{
			AgentID:      o.AgentID,
			AgentVersion: clientName(),
			HostID:       host,
			HostOS:       runtimeOS(),
			// ⚠ The --user flag if given, else what the OS claims. Either way
			// the orchestrator overwrites user_id with the authenticated
			// subject before storing — this only ever travels as a hint.
			UserID:      firstNonEmpty(o.UserID, claimedUser()),
			CollectedAt: time.Now().UTC(),
		},
		Observations: []observation{{
			ObservationID: newObservationID(),
			SourceKind:    "ide_submission",
			SourcePath:    filepath.Base(o.Path),
			Raw:           scrubbed(raw),
			ObservedAt:    time.Now().UTC(),
			Server:        srv,
		}},
	}

	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}

	// ── report what we are about to say, before we say it ──────────────────
	fmt.Printf("submitting %s\n", srv.DeclaredName)
	fmt.Printf("  transport   %s\n", srv.Transport)
	fmt.Printf("  via         %s\n", req.Agent.AgentVersion)
	if req.Agent.UserID != "" {
		// ⚠ Say it is a claim, on screen, where the developer running it can
		// see exactly what is being sent about them. A tool that reports on
		// people should show them what it reports.
		fmt.Printf("  claimed as  %s (unverified)\n", req.Agent.UserID)
	}
	if srv.EndpointURL != "" {
		fmt.Printf("  endpoint    %s\n", srv.EndpointURL)
	}
	if srv.Package != nil {
		fmt.Printf("  package     %s/%s %s\n",
			srv.Package.Ecosystem, srv.Package.Name, srv.Package.VersionSpec)
	}
	if req.Observations[0].Raw != nil {
		fmt.Printf("  manifest    carried through (%d top-level field(s), "+
			"credential-ish values removed)\n", len(req.Observations[0].Raw))
	}
	if len(srv.EnvKeys) > 0 {
		// Names only — worth showing so the developer can SEE that no values
		// are leaving their machine.
		fmt.Printf("  env (names) %s\n", strings.Join(srv.EnvKeys, ", "))
	}
	for _, n := range notes {
		fmt.Printf("  note: %s\n", n)
	}

	if o.DryRun {
		fmt.Printf("\n--dry-run: not sent. Payload:\n%s\n", body)
		return nil
	}

	respBody, status, err := apiRequest(gf, "POST", "/inventory/observations", req)
	if err != nil {
		return fmt.Errorf("could not reach Magertron: %w", err)
	}

	switch status {
	case 200, 202:
		var ir ingestResponse
		_ = json.Unmarshal(respBody, &ir)

		// ⚠ PARTIAL SUCCESS IS THE MODEL. A 202 does not mean everything was
		// taken — per-observation failures come back in rejected[] without
		// failing the batch. Reading only the status would tell a developer
		// their submission landed when it did not.
		if len(ir.Rejected) > 0 {
			for _, r := range ir.Rejected {
				fmt.Printf("\nREJECTED: %s\n", r.Reason)
			}
			return fmt.Errorf("submission was not recorded")
		}

		fmt.Printf("\nsubmitted (request %s)\n", ir.RequestID)
		fmt.Printf("It is now visible to your platform team for review.\n")
		fmt.Printf("⚠ This does NOT deploy it — they decide whether and when.\n")
		return nil

	case 401, 403:
		return fmt.Errorf("not authorized to submit — run: mcpctl login")

	case 500:
		// The one 500 worth explaining rather than dumping.
		if strings.Contains(strings.ToLower(string(respBody)), "not enabled") {
			return fmt.Errorf("developer submissions are not enabled on this " +
				"install — ask your administrator to check the orchestrator " +
				"minted its inventory write credential at startup")
		}
		return fmt.Errorf("server error: %s", strings.TrimSpace(string(respBody)))

	default:
		return fmt.Errorf("unexpected %d: %s", status,
			strings.TrimSpace(string(respBody)))
	}
}

func runtimeOS() string { return runtime.GOOS }

// cmdSubmit — `mcpctl submit <file.mcpb|server.json> [--dry-run] [--user NAME]`
//
// ⚠ Manual flag walking, matching the rest of this CLI. flag.FlagSet stops at
// the first non-flag argument, which would break `submit --dry-run file.json`
// and `submit file.json --dry-run` behaving the same way.
func cmdSubmit(gf globalFlags, args []string) {
	var o submitOpts

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			o.DryRun = true
		case "--user":
			if i+1 < len(args) {
				i++
				o.UserID = args[i]
			}
		case "--help", "-h":
			printSubmitHelp()
			return
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "unknown flag: %s\n", args[i])
				printSubmitHelp()
				os.Exit(2)
			}
			o.Path = args[i]
		}
	}

	if o.Path == "" {
		printSubmitHelp()
		os.Exit(2)
	}
	if _, err := os.Stat(o.Path); err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", o.Path, err)
		os.Exit(1)
	}

	if o.AgentID == "" {
		// Informational only — the orchestrator overwrites it from the
		// authenticated principal. Sent so a dry-run shows the real shape.
		o.AgentID = "mcpctl"
	}

	if err := runSubmit(gf, o); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
		os.Exit(1)
	}
}

func printSubmitHelp() {
	fmt.Print(`mcpctl submit <file> [flags]

Hand a server you have built to the people who govern it.

  <file>        a .mcpb bundle, or a server.json manifest

  --dry-run,-n  show what would be sent, send nothing
  --user NAME   record who submitted it

⚠ SUBMITTING IS NOT DEPLOYING. This records that your server exists so your
platform team can review it. It creates no route and deploys nothing — they
decide whether and when it runs.

Submitting the same server again is a revision: it updates the existing entry
rather than creating a second one.
`)
}
