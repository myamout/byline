package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "Error: DATABASE_URL environment variable is not set")
		os.Exit(1)
	}

	// Migration files are relative to backend/
	migrationsPath := "file://internal/store/migrations"

	m, err := migrate.New(migrationsPath, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating migrator: %v\n", err)
		os.Exit(1)
	}
	defer m.Close()

	command := os.Args[1]

	switch command {
	case "up":
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			fmt.Fprintf(os.Stderr, "Error running migrations up: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Migrations applied successfully")

	case "down":
		steps := 1
		if err := m.Steps(-steps); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			fmt.Fprintf(os.Stderr, "Error rolling back migration: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Rolled back %d migration(s)\n", steps)

	case "version":
		version, dirty, err := m.Version()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting version: %v\n", err)
			os.Exit(1)
		}
		if dirty {
			fmt.Printf("Current version: %d (dirty)\n", version)
		} else {
			fmt.Printf("Current version: %d\n", version)
		}

	case "force":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Error: force command requires a version argument")
			printUsage()
			os.Exit(1)
		}
		var version int
		if _, err := fmt.Sscanf(os.Args[2], "%d", &version); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid version '%s'\n", os.Args[2])
			os.Exit(1)
		}
		if err := m.Force(version); err != nil {
			fmt.Fprintf(os.Stderr, "Error forcing version: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Forced database version to %d\n", version)

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown command '%s'\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: migrate <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  up            Apply all pending migrations")
	fmt.Println("  down          Rollback the last migration")
	fmt.Println("  version       Show current migration version")
	fmt.Println("  force <n>     Force database to version n (use with caution)")
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  DATABASE_URL  PostgreSQL connection string (required)")
}
