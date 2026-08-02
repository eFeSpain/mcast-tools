package main

import (
	"reflect"
	"regexp"
	"testing"
)

func TestPicksSpanishWhenTheSystemIsSpanish(t *testing.T) {
	for _, osLang := range []string{"es", "ES", "es_ES.UTF-8", "es-MX", "es_AR.utf8", "es-419"} {
		got, err := pickLang("auto", osLang)
		if err != nil {
			t.Fatalf("%q: %v", osLang, err)
		}
		if got != langES {
			t.Errorf("%q -> inglés, quiero español", osLang)
		}
	}
}

func TestPicksEnglishForAnyOtherSystem(t *testing.T) {
	// eu (euskera) y et (estonio) empiezan por 'e' pero no son español: el
	// idioma se compara por subetiqueta completa, no por prefijo.
	for _, osLang := range []string{"", "C", "POSIX", "en_US.UTF-8", "fr_FR", "pt_BR", "eu_ES", "et_EE", "esperanto"} {
		got, err := pickLang("auto", osLang)
		if err != nil {
			t.Fatalf("%q: %v", osLang, err)
		}
		if got != langEN {
			t.Errorf("%q -> español, quiero inglés", osLang)
		}
	}
}

func TestLangFlagOverridesDetection(t *testing.T) {
	if got, _ := pickLang("en", "es_ES.UTF-8"); got != langEN {
		t.Error("-lang en no sobrescribe un sistema en español")
	}
	if got, _ := pickLang("es", "en_US.UTF-8"); got != langES {
		t.Error("-lang es no sobrescribe un sistema en inglés")
	}
}

func TestUnknownLangIsAnError(t *testing.T) {
	if _, err := pickLang("fr", "en_US.UTF-8"); err == nil {
		t.Fatal("-lang fr debería ser un error, no una traducción silenciosa al inglés")
	}
}

func TestEnvPrecedence(t *testing.T) {
	cases := []struct{ lcAll, lcMessages, lang, want string }{
		{"es_ES.UTF-8", "en_US.UTF-8", "fr_FR", "es_ES.UTF-8"}, // LC_ALL manda
		{"", "en_US.UTF-8", "es_ES", "en_US.UTF-8"},            // luego LC_MESSAGES
		{"", "", "es_ES", "es_ES"},                             // luego LANG
		{"", "", "", ""},                                       // nada configurado
	}
	for _, c := range cases {
		if got := langFromEnv(c.lcAll, c.lcMessages, c.lang); got != c.want {
			t.Errorf("langFromEnv(%q,%q,%q) = %q, quiero %q", c.lcAll, c.lcMessages, c.lang, got, c.want)
		}
	}
}

// La ayuda de -h se imprime durante flag.Parse, así que -lang hay que leerlo
// de os.Args antes de registrar los flags.
func TestLangIsReadFromArgsBeforeParsing(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-lang", "es"}, "es"},
		{[]string{"--lang", "en"}, "en"},
		{[]string{"-lang=es"}, "es"},
		{[]string{"--lang=en"}, "en"},
		{[]string{"-s", "239.0.10.1:5000", "-lang", "es", "-h"}, "es"},
		{[]string{"-s", "239.0.10.1:5000"}, ""},
		{[]string{}, ""},
	}
	for _, c := range cases {
		if got := langFromArgs(c.args); got != c.want {
			t.Errorf("langFromArgs(%v) = %q, quiero %q", c.args, got, c.want)
		}
	}
}

func TestEveryMessageIsTranslated(t *testing.T) {
	en, es := reflect.ValueOf(msgsEN), reflect.ValueOf(msgsES)
	tp := en.Type()
	for i := 0; i < tp.NumField(); i++ {
		if en.Field(i).String() == "" {
			t.Errorf("%s: sin texto en inglés", tp.Field(i).Name)
		}
		if es.Field(i).String() == "" {
			t.Errorf("%s: sin texto en español", tp.Field(i).Name)
		}
	}
}

// Si una traducción se deja un %s o cambia el orden de los verbos, Printf
// imprime basura en ese idioma y nadie se entera hasta producción.
func TestFormatVerbsMatchAcrossLanguages(t *testing.T) {
	verb := regexp.MustCompile(`%[-+ #0-9.*]*[a-zA-Z]`)
	strip := regexp.MustCompile(`%%`)

	en, es := reflect.ValueOf(msgsEN), reflect.ValueOf(msgsES)
	tp := en.Type()
	for i := 0; i < tp.NumField(); i++ {
		ven := verb.FindAllString(strip.ReplaceAllString(en.Field(i).String(), ""), -1)
		ves := verb.FindAllString(strip.ReplaceAllString(es.Field(i).String(), ""), -1)
		if !reflect.DeepEqual(ven, ves) {
			t.Errorf("%s: verbos distintos entre idiomas: en=%v es=%v", tp.Field(i).Name, ven, ves)
		}
	}
}
