package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"bbbudget/internal/devices"
)

func runPairDevice(_ context.Context, c *cli, args []string) error {
	if _, err := parseArgs(c.flags, args, 0, "pair"); err != nil {
		return err
	}
	registry, err := openDevices(*c.settingsFile)
	if err != nil {
		return err
	}
	pairing, err := registry.CreatePairing()
	if err != nil {
		return err
	}

	return printPairing(c.stdout, pairing)
}

func runDevicesCommand(_ context.Context, c *cli, args []string) error {
	revokeID := c.flags.String("revoke", "", "unpair the device with this id")
	if _, err := parseArgs(c.flags, args, 0, "devices [-revoke ID]"); err != nil {
		return err
	}
	registry, err := openDevices(*c.settingsFile)
	if err != nil {
		return err
	}

	if *revokeID != "" {
		if err = revokeDevice(c.stdout, registry, *revokeID); err != nil {
			return err
		}
	}

	return printDevices(c.stdout, registry)
}

func revokeDevice(w io.Writer, registry *devices.Registry, id string) error {
	if err := registry.Revoke(id); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "revoked %s\n", id)

	return err
}

// openDevices opens the paired devices, kept next to the settings file.
func openDevices(settingsFile string) (*devices.Registry, error) {
	if settingsFile == "" {
		return nil, errors.New("no settings file location for devices.json (set BBBUDGET_SETTINGS)")
	}

	return devices.Open(filepath.Join(filepath.Dir(settingsFile), "devices.json")), nil
}

func printPairing(w io.Writer, pairing devices.Pairing) error {
	minutes := int(devices.PairingTTL.Minutes())
	_, err := fmt.Fprintf(
		w,
		"pairing code: %s\nenter it on the device within %d minutes; it works once\n",
		pairing.Code,
		minutes,
	)

	return err
}

func printDevices(w io.Writer, registry *devices.Registry) error {
	deviceList, err := registry.List()
	if err != nil {
		return err
	}
	if len(deviceList) == 0 {
		_, err = fmt.Fprintln(w, "no paired devices")
		return err
	}

	table := newTable(w)
	if _, err = fmt.Fprintln(table, "ID\tNAME\tPAIRED"); err != nil {
		return err
	}
	for _, device := range deviceList {
		pairedAt := device.PairedAt.Local().Format(timeLayout)
		_, err = fmt.Fprintf(table, "%s\t%s\t%s\n", device.ID, device.Name, pairedAt)
		if err != nil {
			return err
		}
	}

	return table.Flush()
}
