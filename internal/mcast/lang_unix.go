//go:build !windows

package mcast

import "os"

// osLanguage devuelve el idioma del sistema según las variables POSIX.
func osLanguage() string {
	return langFromEnv(os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG"))
}
