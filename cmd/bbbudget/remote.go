package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"bbbudget/internal/devices"
)

func runPair(_ context.Context, c *cli, args []string) error {
	if _, err := parseArgs(c.flags, args, 0, "pair"); err != nil {
		return err
	}
	registry, err := openDevices(*c.settingsFile)
	if err != nil {
		return err
	}

	return printPairing(c.stdout, registry)
}

func runDevices(_ context.Context, c *cli, args []string) error {
	revokeID := c.flags.String("revoke", "", "unpair the device with this id")
	if _, err := parseArgs(c.flags, args, 0, "devices [-revoke ID]"); err != nil {
		return err
	}
	registry, err := openDevices(*c.settingsFile)
	if err != nil {
		return err
	}

	if *revokeID != "" {
		if err = registry.Revoke(*revokeID); err != nil {
			return err
		}
		if _, err = fmt.Fprintf(c.stdout, "revoked %s\n", *revokeID); err != nil {
			return err
		}
	}

	return printDevices(c.stdout, registry)
}

// openDevices opens the paired devices, kept next to the settings file.
func openDevices(settingsFile string) (*devices.Registry, error) {
	if settingsFile == "" {
		return nil, errors.New("no settings file location for devices.json (set BBBUDGET_SETTINGS)")
	}

	return devices.Open(filepath.Join(filepath.Dir(settingsFile), "devices.json")), nil
}

func printPairing(w io.Writer, registry *devices.Registry) error {
	pairing, err := registry.NewPairing()
	if err != nil {
		return err
	}

	minutes := int(devices.PairingTTL.Minutes())
	_, err = fmt.Fprintf(
		w,
		"pairing code: %s\nenter it on the device within %d minutes; it works once\n",
		pairing.Code,
		minutes,
	)

	return err
}

func printDevices(w io.Writer, registry *devices.Registry) error {
	paired, err := registry.List()
	if err != nil {
		return err
	}
	if len(paired) == 0 {
		_, err = io.WriteString(w, "no paired devices\n")
		return err
	}

	var text strings.Builder
	table := newTable(&text)
	rows := "ID\tNAME\tPAIRED\n"
	for _, device := range paired {
		pairedAt := device.PairedAt.Local().Format(timeLayout)
		rows += fmt.Sprintf("%s\t%s\t%s\n", device.ID, device.Name, pairedAt)
	}
	if _, err = io.WriteString(table, rows); err != nil {
		return err
	}
	if err = table.Flush(); err != nil {
		return err
	}

	_, err = io.WriteString(w, text.String())

	return err
}
