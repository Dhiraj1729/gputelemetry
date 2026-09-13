package main

import (
	"context"
	"fmt"
	"gpu-telemetry/internal/storage/postgres"
	"gpu-telemetry/migrations"
	"os"
	"time"
)

func run() error {
	if os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := postgres.Open(ctx, os.Getenv("DATABASE_URL"), 1)
	if err != nil {
		return err
	}
	defer s.Close()
	if err = migrations.Apply(ctx, s.Pool); err != nil {
		return fmt.Errorf("migration failed; verify connectivity, privileges and schema/checksum (database details withheld)")
	}
	fmt.Println("migration 001 applied or already current")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
