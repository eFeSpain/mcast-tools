//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// osLanguage pregunta a Windows por el idioma de interfaz del usuario (p.ej.
// "es-ES"). Si la llamada falla, cae a las variables POSIX, que en un shell
// tipo Git Bash o MSYS sí suelen estar puestas.
func osLanguage() string {
	if langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil && len(langs) > 0 {
		return langs[0]
	}
	return langFromEnv(os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG"))
}
