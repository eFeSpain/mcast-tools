# Avisos de terceros · Third-party notices

**ES** — `mcast-dup` y `mcast-send` se compilan como binarios estáticos, así que
el ejecutable que se distribuye lleva dentro código de terceros. Este fichero
reproduce sus avisos de copyright y el texto de sus licencias, que es lo que
esas licencias exigen al redistribuir en forma binaria. El código propio de
este proyecto está bajo licencia MIT: ver [LICENSE](LICENSE).

**EN** — `mcast-dup` and `mcast-send` are built as static binaries, so the
distributed executable contains third-party code. This file reproduces the
copyright notices and license texts those licenses require when redistributing
in binary form. This project's own code is MIT licensed: see [LICENSE](LICENSE).

---

## The Go Authors — BSD 3-Clause

Aplica a · Applies to:

| Componente · Component | Versión · Version |
|---|---|
| Runtime y biblioteca estándar de Go · Go runtime and standard library | la usada al compilar · whichever builds the binary |
| `golang.org/x/net` | v0.55.0 |
| `golang.org/x/sys` | v0.45.0 |

Los tres distribuyen el mismo texto de licencia, byte a byte (SHA-256
`911f8f5782931320…`), así que se reproduce una sola vez. · All three ship the
same license text, byte for byte, so it appears once.

```
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### Additional IP Rights Grant (Patents)

Los mismos tres componentes distribuyen además esta concesión de patentes. No
impone obligaciones al redistribuidor —es una concesión *a favor* de quien usa
el código—, pero se reproduce por completitud. · The same three components also
ship this patent grant. It imposes no obligation on the redistributor — it is a
grant *in your favour* — but is reproduced for completeness.

```
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

---

## Mantenimiento · Maintenance

**ES** — Si cambian las versiones de `go.mod`, actualiza la tabla. Si se añade
una dependencia con una licencia distinta, añade su bloque con el texto
**literal** del fichero `LICENSE` que trae ese módulo, no una copia de
internet: hay proyectos que modifican la plantilla. El texto exacto está en
`$(go env GOMODCACHE)/<módulo>@<versión>/LICENSE`.

**EN** — When `go.mod` versions change, update the table. When a dependency
with a different license is added, add its block using the **verbatim** text
from that module's own `LICENSE` file, not a copy from the internet: some
projects amend the template. The exact text lives in
`$(go env GOMODCACHE)/<module>@<version>/LICENSE`.
