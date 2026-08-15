package mcast

import (
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Los fuentes van embebidos para que el análisis no dependa del directorio de
// trabajo: así el test también vale ejecutando el binario de test compilado en
// cruzado desde cualquier sitio.
//
//go:embed *.go
var sources embed.FS

// verbCount cuenta los verbos de formato de un mensaje, ignorando los %% .
func verbCount(s string) int {
	verb := regexp.MustCompile(`%[-+ #0-9.*]*[a-zA-Z]`)
	return len(verb.FindAllString(strings.ReplaceAll(s, "%%", ""), -1))
}

// Al venir los formatos de una variable, go vet ya no puede comprobar que los
// argumentos casen con los verbos: un %s de más imprime "%!s(MISSING)" en
// producción y nadie se entera. Esto lo sustituye: parsea el fuente, localiza
// cada llamada de formato cuyo formato sea un txt.algo y compara.
func TestFormatVerbsMatchCallSites(t *testing.T) {
	// función -> posición del argumento de formato
	formatters := map[string]int{
		"Printf": 0, "Sprintf": 0, "Errorf": 0, "Fatalf": 0, "Panicf": 0,
		"Fprintf": 1,
		"reject":  0, // el helper de validación de resolveChannels
	}

	want := map[string]int{}
	en := reflect.ValueOf(msgsEN)
	tp := en.Type()
	for i := 0; i < tp.NumField(); i++ {
		want[tp.Field(i).Name] = verbCount(en.Field(i).String())
	}

	entries, err := sources.ReadDir(".")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no se encuentran los fuentes embebidos: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		file := entry.Name()
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := sources.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		node, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || call.Ellipsis != token.NoPos {
				return true
			}
			var fname string
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				fname = fn.Sel.Name
			case *ast.Ident:
				fname = fn.Name
			default:
				return true
			}
			pos, ok := formatters[fname]
			if !ok || len(call.Args) <= pos {
				return true
			}
			sel, ok := call.Args[pos].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "txt" {
				return true
			}
			field := sel.Sel.Name
			verbs, known := want[field]
			if !known {
				t.Errorf("%s: txt.%s no existe en msgs", fset.Position(call.Pos()), field)
				return true
			}
			checked++
			if got := len(call.Args) - pos - 1; got != verbs {
				t.Errorf("%s: txt.%s tiene %d verbo(s) pero se le pasan %d argumento(s)",
					fset.Position(call.Pos()), field, verbs, got)
			}
			return true
		})
	}
	if checked < 20 {
		t.Fatalf("solo se han comprobado %d llamadas: el análisis no está encontrando el código", checked)
	}
}

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
		// Repetido: gana el último, como haría el paquete flag.
		{[]string{"-lang", "es", "-lang", "en"}, "en"},
		{[]string{"-lang=es", "-lang=en"}, "en"},
		// Tras "--" o tras un posicional, flag ya no parsea: aquí tampoco.
		{[]string{"--", "-lang", "fr"}, ""},
		{[]string{"fichero.json", "-lang", "fr"}, ""},
		// "-lang" como VALOR de otro flag no es una opción.
		{[]string{"-d", "-lang"}, ""},
		{[]string{"-iface", "-lang", "-s", "239.0.10.1:5000"}, ""},
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
