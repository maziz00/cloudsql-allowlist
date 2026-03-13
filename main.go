// cloudsql-allowlist — Manage Cloud SQL authorized networks from the CLI.
//
// Adds, removes, or lists developer IP addresses on a Cloud SQL instance's
// authorized networks (allowlist). Built for teams where developers need
// temporary access to test/staging databases from dynamic IPs.
//
// Authentication: Application Default Credentials (ADC).
// Run: gcloud auth application-default login
//
// Usage:
//
//	cloudsql-allowlist add    --project=<id> --instance=<id> --name=<label> [--ip=<cidr>]
//	cloudsql-allowlist remove --project=<id> --instance=<id> --name=<label>
//	cloudsql-allowlist list   --project=<id> --instance=<id>
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

const (
	version        = "2.0.0"
	defaultTimeout = 60 * time.Second
	ipDetectURL    = "https://api.ipify.org?format=text"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "add":
		runAdd(os.Args[2:])
	case "remove":
		runRemove(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "version":
		fmt.Printf("cloudsql-allowlist v%s\n", version)
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// printUsage prints top-level help text.
func printUsage() {
	fmt.Fprint(os.Stderr, `cloudsql-allowlist — Manage Cloud SQL authorized networks

Usage:
  cloudsql-allowlist <command> [flags]

Commands:
  add     Add an IP address to Cloud SQL authorized networks
  remove  Remove an IP address from Cloud SQL authorized networks
  list    List all current authorized networks on an instance
  version Print version

Authentication:
  Uses Application Default Credentials (ADC).
  Run: gcloud auth application-default login

Run 'cloudsql-allowlist <command> --help' for command flags.
`)
}

// runAdd handles the "add" subcommand.
// It fetches the current authorized networks, checks for duplicates,
// appends the new IP, and PATCHes only the ipConfiguration field.
func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	project  := fs.String("project",  "", "GCP project ID (required)")
	instance := fs.String("instance", "", "Cloud SQL instance ID (required)")
	name     := fs.String("name",     "", "Label for this entry, e.g. 'maziz-home' (required)")
	ip       := fs.String("ip",       "", "IP or CIDR to authorize — auto-detects public IP if omitted")
	dryRun   := fs.Bool("dry-run",  false, "Preview the change without applying it")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: cloudsql-allowlist add [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	must(fs.Parse(args))

	if *project == "" || *instance == "" || *name == "" {
		fs.Usage()
		os.Exit(1)
	}

	// Resolve and normalize the target CIDR.
	cidr, err := resolveIP(*ip)
	fatalf(err, "resolving IP")

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	svc, err := sqladmin.NewService(ctx)
	fatalf(err, "creating Cloud SQL client (run: gcloud auth application-default login)")

	// Fetch the current instance configuration.
	inst, err := svc.Instances.Get(*project, *instance).Context(ctx).Do()
	fatalf(err, "fetching Cloud SQL instance %q", *instance)

	networks := inst.Settings.IpConfiguration.AuthorizedNetworks

	// Guard against adding a duplicate IP or reusing a name.
	for _, n := range networks {
		if n.Value == cidr {
			fmt.Printf("IP %s is already authorized under name %q — nothing to do.\n", cidr, n.Name)
			return
		}
		if n.Name == *name {
			fmt.Printf("Warning: label %q already exists with IP %s. Adding new entry anyway.\n", *name, n.Value)
		}
	}

	// Append the new entry.
	networks = append(networks, &sqladmin.AclEntry{
		Kind:  "sql#aclEntry",
		Name:  *name,
		Value: cidr,
	})

	fmt.Printf("Adding  %-20s %s  →  %s/%s\n", cidr, fmt.Sprintf("(%s)", *name), *project, *instance)

	if *dryRun {
		fmt.Println("[dry-run] No changes applied.")
		return
	}

	// PATCH only ipConfiguration so we do not accidentally reset other settings.
	patch := &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{
			IpConfiguration: &sqladmin.IpConfiguration{
				AuthorizedNetworks: networks,
			},
		},
	}

	op, err := svc.Instances.Patch(*project, *instance, patch).Context(ctx).Do()
	fatalf(err, "patching Cloud SQL instance")

	fmt.Printf("OK — operation %s (%s). Changes propagate in ~30–60 seconds.\n", op.Name, op.Status)
}

// runRemove handles the "remove" subcommand.
// It removes all entries whose Name matches --name.
func runRemove(args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	project  := fs.String("project",  "", "GCP project ID (required)")
	instance := fs.String("instance", "", "Cloud SQL instance ID (required)")
	name     := fs.String("name",     "", "Label of the entry to remove (required)")
	dryRun   := fs.Bool("dry-run",  false, "Preview the change without applying it")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: cloudsql-allowlist remove [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	must(fs.Parse(args))

	if *project == "" || *instance == "" || *name == "" {
		fs.Usage()
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	svc, err := sqladmin.NewService(ctx)
	fatalf(err, "creating Cloud SQL client (run: gcloud auth application-default login)")

	inst, err := svc.Instances.Get(*project, *instance).Context(ctx).Do()
	fatalf(err, "fetching Cloud SQL instance %q", *instance)

	networks := inst.Settings.IpConfiguration.AuthorizedNetworks

	// Filter out matching entries, keeping everything else.
	var kept []*sqladmin.AclEntry
	var removed []string
	for _, n := range networks {
		if n.Name == *name {
			removed = append(removed, fmt.Sprintf("%s (%s)", n.Value, n.Name))
		} else {
			kept = append(kept, n)
		}
	}

	if len(removed) == 0 {
		fmt.Printf("No entry with label %q found — nothing to remove.\n", *name)
		return
	}

	for _, r := range removed {
		fmt.Printf("Removing  %s  ←  %s/%s\n", r, *project, *instance)
	}

	if *dryRun {
		fmt.Println("[dry-run] No changes applied.")
		return
	}

	patch := &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{
			IpConfiguration: &sqladmin.IpConfiguration{
				AuthorizedNetworks: kept,
			},
		},
	}

	op, err := svc.Instances.Patch(*project, *instance, patch).Context(ctx).Do()
	fatalf(err, "patching Cloud SQL instance")

	fmt.Printf("OK — operation %s (%s). Changes propagate in ~30–60 seconds.\n", op.Name, op.Status)
}

// runList handles the "list" subcommand.
// Prints all authorized networks in a table.
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	project  := fs.String("project",  "", "GCP project ID (required)")
	instance := fs.String("instance", "", "Cloud SQL instance ID (required)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: cloudsql-allowlist list [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	must(fs.Parse(args))

	if *project == "" || *instance == "" {
		fs.Usage()
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	svc, err := sqladmin.NewService(ctx)
	fatalf(err, "creating Cloud SQL client (run: gcloud auth application-default login)")

	inst, err := svc.Instances.Get(*project, *instance).Context(ctx).Do()
	fatalf(err, "fetching Cloud SQL instance %q", *instance)

	networks := inst.Settings.IpConfiguration.AuthorizedNetworks

	if len(networks) == 0 {
		fmt.Printf("No authorized networks configured on %s/%s.\n", *project, *instance)
		return
	}

	// Use a tab writer for aligned columns.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "NAME\tIP / CIDR\tKIND\n")
	fmt.Fprintf(w, "----\t---------\t----\n")
	for _, n := range networks {
		fmt.Fprintf(w, "%s\t%s\t%s\n", n.Name, n.Value, n.Kind)
	}
	w.Flush()

	fmt.Printf("\nTotal: %d authorized network(s) on %s/%s\n", len(networks), *project, *instance)
}

// resolveIP returns a normalized CIDR string.
// If ip is empty, it auto-detects the caller's public IP.
// If ip lacks a prefix length, /32 is appended.
func resolveIP(ip string) (string, error) {
	if ip == "" {
		detected, err := detectPublicIP()
		if err != nil {
			return "", fmt.Errorf("auto-detect failed (provide --ip manually): %w", err)
		}
		fmt.Printf("Auto-detected public IP: %s\n", detected)
		ip = detected
	}

	// Append /32 host prefix if caller passed a plain IP.
	if !strings.Contains(ip, "/") {
		ip += "/32"
	}

	// Validate the CIDR is well-formed.
	if _, _, err := net.ParseCIDR(ip); err != nil {
		return "", fmt.Errorf("invalid IP/CIDR %q: %w", ip, err)
	}

	return ip, nil
}

// detectPublicIP calls a lightweight public IP API and returns the result.
func detectPublicIP() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(ipDetectURL)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", ipDetectURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	return strings.TrimSpace(string(body)), nil
}

// fatalf prints an error and exits if err is non-nil.
// context is a short string describing what was being attempted.
func fatalf(err error, context string, args ...any) {
	if err == nil {
		return
	}
	msg := fmt.Sprintf(context, args...)
	fmt.Fprintf(os.Stderr, "error: %s: %v\n", msg, err)
	os.Exit(1)
}

// must panics if err is non-nil. Used only for flag.Parse which already
// exits on error with ExitOnError — this is a belt-and-suspenders guard.
func must(err error) {
	if err != nil {
		panic(err)
	}
}
