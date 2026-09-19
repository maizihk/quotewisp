package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"sentence-api/internal/config"
	"sentence-api/internal/database"
	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/store"
)

func runWebAdmin(args []string) error {
	if len(args) == 0 {
		printWebAdminUsage()
		return usageError("web admin requires a subcommand")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printWebAdminUsage()
		return nil
	}
	switch args[0] {
	case "create":
		return webAdminCreate(args[1:])
	case "list":
		return webAdminList(args[1:])
	case "reset-password":
		return webAdminResetPassword(args[1:])
	case "enable":
		return webAdminSetEnabled(args[1:], true)
	case "disable":
		return webAdminSetEnabled(args[1:], false)
	default:
		return usageError("unknown web admin subcommand")
	}
}

func webAdminCreate(args []string) error {
	fs := flag.NewFlagSet("web admin create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	username := fs.String("username", "", "admin username")
	stdinPass := fs.Bool("password-stdin", false, "read password from stdin")
	if err := fs.Parse(args); err != nil {
		return usageError("invalid arguments")
	}
	if *username == "" || fs.NArg() != 0 {
		return usageError("create requires --username")
	}
	if !*stdinPass {
		return usageError("create requires --password-stdin")
	}
	password, err := readPasswordStdin()
	if err != nil {
		return err
	}
	normalized, err := auth.ValidateUsername(*username)
	if err != nil {
		return errors.New("invalid username")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return errors.New("invalid password")
	}
	c, err := config.Load(config.ModeWebAdmin)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, c.MYSQLDSN, pool(c))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = database.CheckWriteSchema(ctx, db); err != nil {
		return err
	}
	st := store.New(db)
	id, err := st.CreateAdmin(ctx, normalized, hash, nil)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errors.New("username already exists")
		}
		return errors.New("create admin failed")
	}
	fmt.Println(id)
	return nil
}

func webAdminList(args []string) error {
	fs := flag.NewFlagSet("web admin list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError("list accepts no arguments")
	}
	c, err := config.Load(config.ModeWebAdmin)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, c.MYSQLDSN, pool(c))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = database.CheckWriteSchema(ctx, db); err != nil {
		return err
	}
	st := store.New(db)
	admins, err := st.ListAdmins(ctx)
	if err != nil {
		return errors.New("list admins failed")
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUSERNAME\tENABLED\tLAST_LOGIN")
	for _, a := range admins {
		last := "-"
		if a.LastLoginAt != nil {
			last = a.LastLoginAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%d\t%s\t%t\t%s\n", a.ID, a.Username, a.Enabled, last)
	}
	return w.Flush()
}

func webAdminResetPassword(args []string) error {
	fs := flag.NewFlagSet("web admin reset-password", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	username := fs.String("username", "", "admin username")
	stdinPass := fs.Bool("password-stdin", false, "read password from stdin")
	if err := fs.Parse(args); err != nil {
		return usageError("invalid arguments")
	}
	if *username == "" || !*stdinPass || fs.NArg() != 0 {
		return usageError("reset-password requires --username and --password-stdin")
	}
	password, err := readPasswordStdin()
	if err != nil {
		return err
	}
	normalized, err := auth.ValidateUsername(*username)
	if err != nil {
		return errors.New("invalid username")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return errors.New("invalid password")
	}
	c, err := config.Load(config.ModeWebAdmin)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, c.MYSQLDSN, pool(c))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = database.CheckWriteSchema(ctx, db); err != nil {
		return err
	}
	st := store.New(db)
	adminUser, err := st.GetAdminByUsername(ctx, normalized)
	if err != nil {
		return errors.New("admin not found")
	}
	if err = st.SetAdminPasswordHash(ctx, adminUser.ID, hash); err != nil {
		return errors.New("reset password failed")
	}
	return nil
}

func webAdminSetEnabled(args []string, enabled bool) error {
	cmd := "enable"
	if !enabled {
		cmd = "disable"
	}
	fs := flag.NewFlagSet("web admin "+cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	username := fs.String("username", "", "admin username")
	if err := fs.Parse(args); err != nil {
		return usageError("invalid arguments")
	}
	if *username == "" || fs.NArg() != 0 {
		return usageError(cmd + " requires --username")
	}
	normalized, err := auth.ValidateUsername(*username)
	if err != nil {
		return errors.New("invalid username")
	}
	c, err := config.Load(config.ModeWebAdmin)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, c.MYSQLDSN, pool(c))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = database.CheckWriteSchema(ctx, db); err != nil {
		return err
	}
	st := store.New(db)
	adminUser, err := st.GetAdminByUsername(ctx, normalized)
	if err != nil {
		return errors.New("admin not found")
	}
	if err = st.SetAdminEnabled(ctx, adminUser.ID, enabled); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			return errors.New("cannot disable the last enabled admin")
		}
		return errors.New(cmd + " failed")
	}
	return nil
}

func readPasswordStdin() (string, error) {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxPasswordBytes+2))
	if err != nil {
		return "", errors.New("read password failed")
	}
	password := strings.TrimSuffix(string(data), "\n")
	password = strings.TrimSuffix(password, "\r")
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	return password, nil
}

const maxPasswordBytes = 512

type usageErr struct{ msg string }

func (e usageErr) Error() string { return e.msg }

func usageError(msg string) error { return usageErr{msg: msg} }

func printWebAdminUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  sentence-api web admin create --username <name> --password-stdin")
	fmt.Fprintln(os.Stderr, "  sentence-api web admin list")
	fmt.Fprintln(os.Stderr, "  sentence-api web admin reset-password --username <name> --password-stdin")
	fmt.Fprintln(os.Stderr, "  sentence-api web admin enable --username <name>")
	fmt.Fprintln(os.Stderr, "  sentence-api web admin disable --username <name>")
}
