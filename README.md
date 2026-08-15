# mcast-tools

**Español** · [English](README.en.md)

[![CI](https://github.com/eFeSpain/mcast-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/eFeSpain/mcast-tools/actions/workflows/ci.yml)

Dos herramientas de línea de órdenes para operar multicast UDP en redes de
vídeo. Binarios estáticos sin dependencias en tiempo de ejecución, N canales por
proceso y recarga en caliente con SIGHUP.

| | |
|---|---|
| **[`mcast-dup`](#mcast-dup)** | Duplica: recibe un grupo y reenvía cada datagrama tal cual a uno o varios grupos distintos, sin recodificar. |
| **[`mcast-send`](#mcast-send)** | Emite: manda un fichero, la entrada estándar o un patrón generado a un grupo, al bitrate que le pidas. |

```
                    ┌──►  239.255.0.1:1234
fichero.ts          │
   │                │
   ▼                │
mcast-send ──► 239.0.10.1:5000 ──► mcast-dup ──┤
                                               │
                                               └──►  239.255.1.1:1234
```

Comparten `internal/mcast`: configuración, resolución de interfaz, sockets,
orquestación de canales, estadísticas y mensajes. Un arreglo en la capa común
llega a los dos, pero cada binario se instala y se ejecuta por separado.

---

<a name="mcast-dup"></a>

## mcast-dup

Recibe un grupo y reenvía cada datagrama tal cual a uno o varios grupos
distintos. Sin recodificar y sin tocar el payload.

```
239.0.10.1:5000  ──►  mcast-dup  ──┬──►  239.255.0.1:1234
                                   └──►  239.255.1.1:1234
```

## Para qué sirve

Para reencaminar flujos entre planes de direccionamiento distintos: el sistema
que emite usa `239.0.10.x` y el que consume espera `239.255.x.x`, o hace falta
llevar el mismo canal a dos destinos a la vez, o exponer una copia en un rango
que sí atraviesa cierto router. Casos típicos de operación IPTV, SDI-over-IP y
distribución interna de señal.

## Qué no es

| Si lo que quieres es… | Usa |
|---|---|
| repetir el **mismo** grupo en otra interfaz o VLAN | [udp-broadcast-relay-redux](https://github.com/udp-redux/udp-broadcast-relay-redux), [alsmith/multicast-relay](https://github.com/alsmith/multicast-relay) |
| servir multicast por HTTP a clientes unicast | [udpxy](https://github.com/tydaikho/udpxy) |
| enrutar multicast entre interfaces (PIM/IGMP) | smcroute, igmpproxy |
| mover un flujo suelto desde la shell | `multicat` (VideoLAN), `socat` |

`mcast-dup` se ocupa de **reescribir el grupo de destino**, que es lo que
ninguno de esos hace, y de llevar muchos canales a la vez con recarga sin
cortar los que no cambian.

## Compilar

```sh
go mod download
CGO_ENABLED=0 go build -o mcast-dup  ./cmd/mcast-dup
CGO_ENABLED=0 go build -o mcast-send ./cmd/mcast-send

# cruzado, para un servidor Linux
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mcast-dup ./cmd/mcast-dup
```

## Uso

### Un canal, desde la línea de órdenes

```sh
mcast-dup -s 239.0.10.1:5000 -d 239.255.0.1:1234,239.255.1.1:1234 \
          -iface 10.30.0.5 -ttl 8
```

| Opción | Por defecto | Qué hace |
|---|---|---|
| `-s` | — | origen `GRUPO:PUERTO` (obligatorio) |
| `-d` | — | destino(s) `GRUPO:PUERTO`, separados por comas (obligatorio) |
| `-iface` | auto | NIC para recibir y emitir: IP local (`10.30.0.5`) o nombre (`eth0`) |
| `-ttl` | 8 | TTL multicast de salida |
| `-loop` | true | loopback multicast local |
| `-rcvbuf` | 4 MiB | `SO_RCVBUF` del socket de recepción |
| `-sndbuf` | 0 | `SO_SNDBUF` (0 = no tocarlo) |
| `-stats` | 10 | segundos entre resúmenes (0 los apaga) |
| `-watchdog` | 60 | segundos sin recibir nada antes de rehacer el socket (0 lo desactiva) |
| `-logfile` | — | escribir los logs a un fichero en vez de stdout/stderr |
| `-lang` | auto | idioma de los mensajes: `auto`, `es` o `en` |

### Idioma de los mensajes

Los mensajes, los errores y la ayuda salen en el idioma del sistema: **español
si el sistema está en español, inglés en cualquier otro caso**. En Linux y
macOS se mira `LC_ALL`, `LC_MESSAGES` y `LANG`, por ese orden; en Windows se
pregunta por el idioma de interfaz del usuario.

Bajo systemd `LANG` suele venir vacío, así que los logs saldrán en inglés. Si
los quieres en español, `-lang es` en el `ExecStart` o `Environment=LANG=es_ES.UTF-8`
en la unidad.

### Varios canales, modo daemon

```sh
mcast-dup -config /etc/mcast-dup.json
kill -HUP $(pidof mcast-dup)      # recargar sin cortar los canales intactos
```

Ver [`mcast-dup.example.json`](mcast-dup.example.json).

## Configuración

`defaults` se aplica a todos los canales y cada canal puede sobrescribir lo que
necesite.

| Campo | Ámbito | Por defecto | Qué hace |
|---|---|---|---|
| `iface` | ambos | auto | NIC para rx y tx: IP local o nombre de la interfaz |
| `ttl` | ambos | 8 | TTL multicast de salida |
| `loop` | ambos | true | loopback multicast local |
| `rcvbuf` | ambos | 4194304 | `SO_RCVBUF` en bytes |
| `sndbuf` | ambos | 0 | `SO_SNDBUF` en bytes (0 = no tocarlo) |
| `stats` | defaults | 10 | segundos entre resúmenes (0 los apaga) |
| `watchdog` | ambos | 60 | segundos sin recibir nada antes de rehacer el socket (0 lo desactiva) |
| `name` | canal | el `source` | nombre en los logs; identifica el canal en las recargas |
| `source` | canal | obligatorio | `GRUPO:PUERTO` de origen, tiene que ser multicast |
| `dest` | canal | obligatorio | lista de `GRUPO:PUERTO` destino (también vale unicast) |

### Filtrar por emisor (SSM)

Con `from` en un canal, solo se acepta el grupo si viene de esos emisores:

```json
{ "name": "la1-hd", "source": "232.1.2.3:5000", "dest": ["239.255.0.1:1234"],
  "from": ["10.20.30.40"] }
```

Se hace en dos capas. Primero se intenta un **join por fuente** (SSM, RFC 4607):
el kernel filtra, y con IGMPv3 en la red el switch ni siquiera nos manda el
tráfico de otros emisores. Y además se **comprueba siempre aquí**, en el bucle
de recepción, porque si la red no habla IGMPv3 el kernel puede entregar tráfico
de otros de todas formas.

Esto tapa un agujero que el filtro por destino no cubre: aquel bloquea lo que no
va dirigido al grupo, pero no hace nada contra un **emisor equivocado emitiendo
al grupo correcto** — el encoder de respaldo mal configurado que emite en
paralelo, y el decodificador viendo errores de continuidad imposibles de
explicar.

En Windows `x/net` no implementa el join por fuente: allí se une al grupo entero
y filtra en userspace, avisando por log de que no está cortando el tráfico
aguas arriba.

### Qué se valida, al arrancar y en cada recarga

- El origen tiene que ser una dirección multicast.
- Las direcciones de `from` tienen que ser unicast: un grupo o un `0.0.0.0` ahí
  no filtran nada.
- El `ttl` tiene que estar en `[0,255]`. Fuera de rango el kernel rechaza la
  opción y el socket se queda en TTL 1 sin que nadie se entere, así que el
  canal se rechaza en vez de emitir en silencio donde no debe.
- Se rechaza cualquier canal que **cree un bucle de realimentación**: un
  destino que vuelva, directa o indirectamente, al origen del propio canal.
  Con el loopback activado eso multiplica el flujo en cada vuelta hasta saturar
  la NIC. Cuenta también el bucle que se cuela por la puerta del unicast: un
  destino como `127.0.0.1:5000` o `<IP-de-esta-máquina>:5000`, cuando 5000 es
  el puerto de origen de algún canal, vuelve a entrar por el socket de
  recepción exactamente igual. Una cascada legítima (el canal A alimenta el
  grupo que lee el canal B) sí se permite.
- Las direcciones tienen que llevar puerto. Un `239.0.10.1:0` se aceptaba y
  luego bindeaba a un puerto efímero: el canal se unía al grupo y no recibía un
  solo datagrama, con aspecto de estar sano.
- Un `stats` disparatado se acota a 24 h en vez de desbordar el temporizador.
- Los destinos repetidos dentro de un canal se descartan con aviso: emitirían
  cada paquete dos veces al mismo sitio.
- Los canales con nombre duplicado o direcciones inválidas se descartan con un
  aviso, sin tumbar a los demás. Si un canal no lleva `name`, se le pone el de
  su `source`: un nombre derivado de la posición en el array haría que insertar
  un canal renombrara a los siguientes y el SIGHUP los reiniciara todos.
- Los campos que el programa no conoce se avisan pero **no** son fatales: un
  `"defualts"` mal escrito perdería el bloque entero en silencio, y ahora se
  ve en el log; pero una clave tipo `"_comment"` (JSON no tiene comentarios)
  no tumba la configuración.

En modo flags cualquier problema es fatal y el proceso sale con código 2. En
modo daemon se ignora el canal afectado y el resto sigue.

### Recarga

`SIGHUP` arranca los canales nuevos, para los que desaparecen y reinicia solo
los que han cambiado, esperando a que el canal viejo cierre sus sockets antes
de arrancar el relevo.

SIGHUP hace además lo que se espera convencionalmente de esa señal: **reabre el
fichero de `-logfile`**. Sin eso, tras un `logrotate` el proceso seguiría
escribiendo al inodo viejo hasta el siguiente reinicio. En modo flags no hay
nada que recargar, así que SIGHUP solo reabre el log y lo dice — antes mataba
el proceso.

Nada de lo que ya está emitiendo se corta por un error de edición:

- Un JSON inválido no tumba nada: se avisa y sigue en pie la configuración
  anterior.
- Un canal que **sí sigue en el fichero pero cuya configuración nueva no
  valida** se queda como estaba, emitiendo, y se avisa por log. Solo se paran
  los canales que desaparecen del fichero, que es una decisión deliberada.
- Con una excepción: si conservarlo **chocaría con los canales que sí han
  validado**, se para y se explica. Conservar a ciegas reintroduciría por la
  puerta de atrás justo lo que la validación acaba de rechazar — basta con
  intercambiar dos destinos y equivocarse en el segundo canal para acabar con
  un bucle de realimentación en el relé, o con dos canales del emisor apuntando
  al mismo grupo.

### Las estadísticas

```
[13:54:42] la1-hd              950 pkt/s · rx   9.98 Mbps · tx  29.94 Mbps · 0 err · 3 drop
```

`rx` es lo que entra y `tx` lo que sale de verdad, medido: con tres destinos el
enlace lleva el triple de lo que se recibe, y es `tx` lo que hay que comparar
con la capacidad del enlace.

`err` y `drop` son **cuentas del intervalo, no tasas**, a propósito: son sucesos
raros y como tasa se redondearían a cero — un error cada diez segundos son
`0,1 err/s`, que se imprime `0` y no lo ves nunca. `err` son fallos de envío y
`drop`, datagramas descartados por no ir dirigidos al grupo del canal.

Cuando hay algún `err`, la siguiente línea del log trae el motivo real del
último fallo (`network is unreachable` y compañía), no solo el contador.

### Vigilancia de recepción (`watchdog`)

Si la NIC se recrea con otro ifindex o cambia de IP, la pertenencia al grupo
queda huérfana y el socket no vuelve a recibir nada, para siempre y en
silencio. Con `watchdog` segundos sin un solo datagrama, el canal rehace el
socket, vuelve a resolver la interfaz y repite el join. El aviso se registra
**una vez por episodio**, no en cada reintento, para que un canal apagado de
madrugada no llene el log.

Ponlo a 0 si tienes canales que legítimamente pasan horas mudos y prefieres no
tocar nada.

---

<a name="mcast-send"></a>

## mcast-send

Emite un flujo multicast al bitrate que le pidas. Tres orígenes posibles:

```sh
# un fichero, en bucle
mcast-send -d 239.0.10.1:5000 -f barras.ts -b 10M -iface eth0

# lo que le llegue por la entrada estándar
ffmpeg -re -i entrada.mp4 -c copy -f mpegts - | mcast-send -d 239.0.10.1:5000 -stdin -b 8M

# un patrón numerado, sin necesidad de material
mcast-send -d 239.0.99.1:5000 -b 2M
```

| Opción | Por defecto | Qué hace |
|---|---|---|
| `-d` | — | destino `GRUPO:PUERTO` (obligatorio) |
| `-f` | — | fichero a emitir |
| `-stdin` | false | leer de la entrada estándar |
| `-b` | 10M | bitrate: bits/s o con sufijo (`10M`, `512k`, `2.5M`) |
| `-size` | 1316 | bytes de payload por datagrama (1316 = 7 paquetes TS) |
| `-loop-file` | true | volver a empezar el fichero al terminarlo |
| `-iface`, `-ttl`, `-loop`, `-sndbuf`, `-stats`, `-logfile`, `-lang` | | como en `mcast-dup` |

Sin `-f` ni `-stdin` emite un **patrón numerado**: cada datagrama lleva su
número de secuencia en los 8 primeros bytes, así que en el otro extremo se puede
comprobar que no falta ninguno, que no se repiten y que llegan en orden. Es lo
que usa la prueba automática del repositorio.

### Es un bombeador de bytes, no un multiplexor

`mcast-send` trocea y pacea; no parsea nada. Con eso cubre el caso normal de
IPTV —MPEG-TS sobre UDP crudo— pero conviene saber qué **no** hace: ni RTP, ni
FEC (SMPTE 2022), ni SRT/RIST, ni remultiplexado, ni transcodificación, ni
bitrate variable guiado por el PCR. Si le pides 10 Mbps a un TS que en realidad
son 6, lo emitirás 1,6 veces más rápido y le reventarás el búfer al
decodificador: el bitrate correcto lo pones tú.

Por eso el tamaño por defecto es **1316 = 7 × 188**, un número entero de
paquetes TS. Si el material parece un transport stream —se comprueba mirando los
bytes de sincronismo `0x47`— y la alineación no cuadra, se avisa:

```
config: channel 'barras': /srv/barras.ts looks like MPEG-TS, but the datagram
        size (1400) is not a multiple of 188: TS packets would be split across
        datagrams and no decoder could read the stream
```

Y lo mismo si la **longitud del fichero** no es múltiplo de 188: cada vuelta del
bucle emitiría un paquete cortado. Son avisos y no rechazos, porque emitir algo
que no sea TS con el tamaño que quieras es un uso legítimo — y precisamente por
eso el aviso solo aparece cuando el fichero parece TS de verdad.

Es la clase de fallo más traicionera: el flujo sale a su bitrate, con cero
errores en las estadísticas, y sencillamente no hay decodificador que lo lea.

### El modo daemon es igual que el del relé

```sh
mcast-send -config /etc/mcast-send.json
systemctl reload mcast-send
```

Ver [`mcast-send.example.json`](mcast-send.example.json). Mismas reglas:
`defaults` más overrides por canal, recarga sin cortar lo que no cambia, un
canal cuya config nueva no valida se queda como estaba, y validación por
adelantado de lo que no puede funcionar (destino sin puerto, bitrate ilegible,
fichero que no se puede leer, dos canales al mismo destino).

| Campo | Ámbito | Por defecto | Qué hace |
|---|---|---|---|
| `dest` | canal | obligatorio | `GRUPO:PUERTO` al que emitir |
| `file` | canal | — | fichero a emitir |
| `loop` | canal | true | repetir el fichero al acabarlo |
| `stdin` | canal | false | leer de la entrada estándar |
| `bitrate` | ambos | `10M` | bits/s o con sufijo |
| `size` | ambos | 1316 | bytes de payload por datagrama |
| `iface`, `ttl`, `loopback`, `sndbuf`, `stats` | ambos | | como en el relé |

### Por qué emitir es más difícil que reenviar

El relé va **paceado por su entrada**: reenvía cuando llega algo, y el reloj lo
pone el emisor original. Un emisor tiene que **poner su propio reloj**, y ahí
está toda la dificultad.

Un TS a 10 Mbps con payload de 1316 bytes son ~950 paquetes/s: uno cada 1,05 ms.
Dormir «un intervalo» en cada vuelta no vale, porque la deriva de cada sueño se
acumula y en una hora vas minutos desfasado. `mcast-send` usa un **reloj
absoluto**: calcula cuándo debería salir el paquete N contando desde el
arranque, así el error de un sueño se corrige solo en la vuelta siguiente.

Medido con los binarios reales: pedidos 4 Mbps, emitidos 380 pkt/s × 1316 B =
**4,00 Mbps**, y el relé al otro lado marcando `rx 4.00 · tx 4.00`.

---

## systemd

```sh
install -m 0755 mcast-dup mcast-send /usr/local/bin/
install -m 0644 mcast-dup.example.json  /etc/mcast-dup.json
install -m 0644 mcast-send.example.json /etc/mcast-send.json
install -m 0644 mcast-dup.service mcast-send.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now mcast-dup
systemctl reload mcast-dup        # tras editar /etc/mcast-dup.json
```

Cada herramienta tiene su unidad, su fichero de configuración y su ciclo de
vida: en un servidor que solo reemite no hace falta instalar el emisor.

## Ajustes de red

`rcvbuf` y `sndbuf` los recorta el kernel a `net.core.rmem_max` /
`net.core.wmem_max`. Para que 4 MiB sean 4 MiB de verdad:

```sh
sysctl -w net.core.rmem_max=8388608
sysctl -w net.core.wmem_max=8388608
```

Si ves pérdidas, ese suele ser el primer sitio donde mirar, junto con
`netstat -su | grep -i 'receive errors'`.

## El detalle que casi todos los relés multicast se dejan

Si dos canales comparten el puerto de origen con grupos distintos
(`239.0.10.1:5000` y `239.0.20.1:5000`, cosa normalísima en IPTV), en Linux
**cada socket recibe también el tráfico del otro** y lo reenvía a su propio
destino: el flujo sale cruzado y duplicado.

La causa es `IP_MULTICAST_ALL`, que vale 1 por defecto: un socket bindeado al
comodín recibe *todos* los grupos unidos en la máquina que lleguen a su puerto,
no solo los que ese socket unió. `mcast-dup` lo pone a 0 (ver
[`control_linux.go`](control_linux.go)).

Lo que **no** sirve como alternativa en Go es bindear al grupo en vez de al
comodín: el paquete `net` reescribe cualquier bind multicast a `0.0.0.0`
(`listenDatagram`, en `src/net/sock_posix.go`). Ese arreglo compila, parece
correcto y no hace absolutamente nada.

Medido en Linux con dos receptores en el puerto 5000, unidos a grupos
distintos, enviando un paquete al grupo A:

| Opciones del socket RX | A recibe | B recibe |
|---|---|---|
| `SO_REUSEADDR` | sí | **sí** ← mezcla |
| `SO_REUSEADDR` + `SO_REUSEPORT` | sí | **sí** ← mezcla |
| `SO_REUSEADDR` + `IP_MULTICAST_ALL=0` | sí | no |

Windows y los BSD no necesitan nada de esto: entregan a cada socket solo los
grupos que ese socket unió. Comprobado en Windows; en BSD es el comportamiento
que precisamente motivó que Linux añadiera la opción.

### Y el mismo bind al comodín deja otra puerta abierta

`IP_MULTICAST_ALL=0` arregla la mezcla **entre grupos**, pero no toca el
unicast: un socket en `0.0.0.0:5000` sigue recibiendo lo que se mande a la IP
de la máquina en ese puerto, y el broadcast. Sin mirar la dirección de destino,
el relé reenviaría ese tráfico ajeno dentro del grupo multicast. Medido: tres
datagramas unicast enviados a `<IP-del-relé>:5000` aparecían íntegros en el
grupo destino.

Por eso el socket de recepción pide la dirección de destino de cada datagrama
(`IP_PKTINFO`, vía `SetControlMessage(ipv4.FlagDst, true)`) y descarta lo que no
venga dirigido al grupo del canal. Los descartes se cuentan y salen en las
estadísticas como `drop/s`, así que un `ffmpeg` mal apuntado o un escaneo UDP se
ven en el log en vez de acabar dentro del transport stream.

En Windows esto no es posible: `x/net/ipv4` no implementa `SetControlMessage`
(devuelve `errNotImplemented`), así que allí el filtro se desactiva y el
arranque lo avisa explícitamente por canal.

## Limitaciones

- **Solo IPv4.**
- **Sin SSM ni filtrado por fuente** (IGMPv3): no se puede pedir «este grupo,
  pero solo del emisor X».
- **Una syscall por paquete y destino.** No usa `recvmmsg`/`sendmmsg`. Sobra
  para decenas de canales; si necesitas cientos, el techo está aquí.
- **Linux es la plataforma principal.** Compila y funciona en Windows y
  macOS/BSD, pero en Windows no existe `SIGHUP` (no hay recarga en caliente) ni
  se puede filtrar por dirección de destino (el arranque lo avisa).
- El camino de datos **sí** está cubierto: la suite levanta `mcast-send`, un
  `mcast-dup` y un receptor en el mismo proceso y comprueba que el patrón
  numerado llega entero, sin duplicados, sin huecos y sin mezclarse con otro
  grupo. Se salta sola si la máquina no tiene una NIC con multicast.

## Licencia

MIT. Ver [LICENSE](LICENSE).
