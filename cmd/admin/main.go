// Command admin manages staff accounts from the terminal.
//
//	go run ./cmd/admin create -role recruiter -name "Sinta Dewi" -email sinta@company.com
//	go run ./cmd/admin promote -role admin -email someone@company.com
//
// The password is read from the ADMIN_PASSWORD env var or prompted for, never passed as a flag
// (flags end up in shell history).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/app"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/config"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	role := fs.String("role", "recruiter", "recruiter or admin")
	name := fs.String("name", "", "full name")
	email := fs.String("email", "", "email address")
	_ = fs.Parse(os.Args[2:])
	if *role != "recruiter" && *role != "admin" {
		fail("role must be recruiter or admin")
	}

	cfg, err := config.Load()
	if err != nil {
		fail(err.Error())
	}
	ctx := context.Background()
	st, err := app.OpenStore(ctx, cfg, app.Logger(cfg))
	if err != nil {
		fail(err.Error())
	}
	defer st.Close()

	switch cmd {
	case "create":
		pw := os.Getenv("ADMIN_PASSWORD")
		if pw == "" {
			fmt.Print("Password (min 12 characters, input is visible): ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			pw = strings.TrimRight(line, "\r\n")
		}
		u, err := app.CreateStaff(ctx, st, *name, *email, pw, domain.Role(*role))
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("Created %s %s (%s)\n", u.Role, u.FullName, u.Email)
	case "promote":
		u, err := st.GetUserByEmail(ctx, *email)
		if err != nil {
			fail("user not found")
		}
		if err := st.SetUserRole(ctx, u.ID, domain.Role(*role)); err != nil {
			fail(err.Error())
		}
		fmt.Printf("%s is now %s\n", u.Email, *role)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: admin create|promote -role recruiter|admin -email EMAIL [-name NAME]")
	os.Exit(2)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}
