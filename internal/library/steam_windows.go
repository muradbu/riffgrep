package library

import "golang.org/x/sys/windows/registry"

// Steam records where it's installed in the registry.
func platformSteamRoots() []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	p, _, err := k.GetStringValue("SteamPath")
	if err != nil || p == "" {
		return nil
	}
	return []string{p}
}
