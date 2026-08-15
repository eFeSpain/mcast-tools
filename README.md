# mcast-tools

**Español** · [English](README.en.md)

[![CI](https://github.com/eFeSpain/mcast-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/eFeSpain/mcast-tools/actions/workflows/ci.yml)

Dos herramientas de línea de órdenes para operar multicast UDP en redes de
vídeo. Binarios estáticos sin dependencias en tiempo de ejecución, N canales por
proceso y recarga en caliente con SIGHUP.

| | |
|---|---|
| **[`mcast-dup`](#mcast-dup)** | Duplica: recibe un grupo y reenvía cada datagrama tal cual a uno o varios grupos distintos, sin recodificar. |
| **[`mcast-send`](#mcast-send)** | Emite: manda un fichero, la salida de una orden externa, la entrada estándar o un patrón generado, con RTP opcional y al ritmo que marque el PCR del propio flujo. |

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
| `from` | canal | — | emisores de los que se acepta el grupo; vacío acepta cualquiera. Ver [Filtrar por emisor (SSM)](#filtrar-por-emisor-ssm) |

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

Emite un flujo multicast, en UDP crudo o encapsulado en RTP, al bitrate que le
pidas o al que marque el reloj del propio flujo. Cuatro orígenes posibles:

```sh
# un fichero TS, en bucle
mcast-send -d 239.0.10.1:5000 -f barras.ts -b 10M -iface eth0

# cualquier otro formato: ffmpeg lo remultiplexa y mcast-send lo supervisa
mcast-send -d 239.0.10.1:5000 -b 8M \
           -exec "ffmpeg -v quiet -re -i entrada.mp4 -c copy -f mpegts -muxrate 8000000 -"

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
| `-exec` | — | ejecutar esta orden y emitir su salida estándar |
| `-rtp` | false | encapsular en RTP (RFC 3550, PT 33) |
| `-b` | 10M | bitrate: bits/s, con sufijo (`10M`, `512k`) o `pcr` |
| `-size` | 1316 | bytes de payload por datagrama (1316 = 7 paquetes TS) |
| `-loop-file` | true | volver a empezar el fichero al terminarlo |
| `-iface`, `-ttl`, `-loop`, `-sndbuf`, `-stats`, `-logfile`, `-lang` | | como en `mcast-dup` |

Sin `-f`, `-exec` ni `-stdin` emite un **patrón numerado**: cada datagrama lleva su
número de secuencia en los 8 primeros bytes, así que en el otro extremo se puede
comprobar que no falta ninguno, que no se repiten y que llegan en orden. Es lo
que usa la prueba automática del repositorio.

### Cuándo termina, y qué dice mientras emite

Con `-loop-file=false`, o cuando se cierra la tubería de `-stdin`, el canal ha
hecho su trabajo: se registra como terminado y, si no queda ningún otro canal
emitiendo, **el proceso sale con código 0**. No se queda dando vueltas ni
reabre la fuente, que es lo que haría si confundiera «he acabado» con «me he
caído».

El resumen del emisor no lleva las columnas del relé, porque no recibe nada:

```
[13:54:42] barras              950 pkt/s · tx   9.98 Mbps · 0 err · 0 rebase
```

`rebase` cuenta las veces que hubo que **rebasar el reloj**: mover el ancla al
momento actual en vez de intentar recuperar el desfase de golpe. Pasa cuando la
fuente no da el bitrate pedido —un disco lento, ffmpeg tardando en arrancar, un
bitrate configurado por encima de lo que el material da de sí— y también cuando
el PCR del flujo pega un salto.

Recuperar el retraso a lo bruto sería peor que el retraso: son los bytes
atrasados saliendo de golpe a velocidad de cable, justo lo que desborda el búfer
del decodificador. Por eso se rebasa.

Un `rebase` **por episodio** es lo normal. Un contador que sube sin parar
significa que estás pidiendo más de lo que tu fuente puede entregar; el flujo
sale igual, pero no al ritmo que crees. La línea siguiente del log trae el
desfase del último.

### Otros formatos: `exec`

Por UDP multicast viaja MPEG-TS. Un MP4, un MKV o un MOV **no se pueden emitir
tal cual**: saldrían a su bitrate, con cero errores en las estadísticas, y no
habría decodificador capaz de leerlos. Así que si le das uno, se rechaza con la
orden exacta para arreglarlo:

```
canal 'peli': /srv/peli.mp4 es MP4/MOV, y por UDP multicast viaja MPEG-TS.
              Remultiplexa sin recodificar:
              ffmpeg -i /srv/peli.mp4 -c copy -f mpegts /srv/peli.ts
```

Y para no tener que preparar el material a mano, `exec` lanza esa conversión
**bajo supervisión**:

```json
{ "name": "peli", "dest": "239.0.10.3:5000", "bitrate": "6M",
  "exec": "ffmpeg -v quiet -re -i /srv/media/peli.mp4 -c copy -f mpegts -" }
```

La orden vive y muere con el canal: si se cae, el canal se cae con ella y se
reintenta como cualquier otro; si paras el canal, se mata la orden — comprobado
que no quedan procesos huérfanos. Y cuando muere, el log trae **su última
salida de error**, que es lo que necesitas para saber qué le pasó:

```
[peli] relé caído (origen de datos: la orden ha fallado (exit status 1);
       su última salida: No such file or directory); reintento en 3s
```

Cada canal lanza la suya, así que esto **sí escala a multicanal**, a diferencia
de `-stdin`, del que solo puede tirar un canal por proceso.

La orden no pasa por un intérprete: se parte respetando comillas y se ejecuta
directamente. No hay expansión de `*`, ni tuberías, ni `&&` — y tampoco hay
inyección desde el fichero de configuración. Lo que sí puede hacer quien
escriba el JSON es ejecutar el binario que quiera: `exec` da el control del
proceso, así que el fichero de configuración merece los mismos permisos que el
propio servicio.

#### La orden de ffmpeg que hace falta para señal de emisión

`-c copy -f mpegts` arregla el **formato**, que es lo que hace legible el
flujo. La **temporización** la deja a medias, y eso no se ve hasta que lo
mides. Medido sobre 20 s de H.264 25 fps con GOP de 2 s más AAC:

| | `-c copy -f mpegts` | añadiendo `-muxrate 6000000` | Referencia |
|---|---|---|---|
| Repetición de PCR | 80,0 ms — **100 % fuera** | 20,0 ms — 0 % fuera | ≤ 40 ms |
| Extensión de 27 MHz del PCR | **0 en las 250 muestras** | presente en 960 de 1004 | imprescindible |
| Tasa entre PCR | 4,06 – 8,33 Mbps → **oscila 2,1×** | 6,00 – 6,00 Mbps → 1,0× | constante |
| Paquetes nulos | 0 | 8716 (10,9 %) | los que hagan falta |

La segunda fila es la que decide: **sin `-muxrate`, ffmpeg tampoco calcula la
extensión de 27 MHz del PCR**. Sin ella el reloj queda cuantizado a 90 kHz —
11,1 µs—, veintidós veces por encima de la tolerancia de PCR_accuracy que pide
TR 101 290. Con `-muxrate` la calcula. No basta para ser conforme, pero sin
ella es imposible.

Así que la orden para emisión es:

```sh
ffmpeg -v quiet -re -i peli.mp4 -c copy -f mpegts \
       -muxrate 6000000 -mpegts_flags +system_b -
```

Y poco más hace falta: `-pat_period` (0,1 s) y `-sdt_period` (0,5 s) ya vienen
bien de fábrica, y `-pcr_period 20` es redundante —con `-muxrate` la salida es
byte a byte idéntica con él y sin él—. `+system_b` es señalización DVB en vez
de la ATSC por defecto.

**El único parámetro delicado es el propio `-muxrate`, y quedarse corto es peor
que no ponerlo.** Pidiéndole 4M a ese mismo flujo de 5,36, ffmpeg avisa
`dts < pcr, TS is invalid` y estira la línea de tiempo: 20 s de material
salieron repartidos en 26,9 s. Y lo traicionero es que las métricas de
conformidad salen impecables —PCR cada 19,9 ms, tasa clavada en 4,00 Mbps— con
el flujo roto. Ponlo con holgura sobre la suma de los flujos elementales; lo
que sobre se rellena con nulos.

Con la salida ya en CBR, `"bitrate": "pcr"` hace lo que promete: el ritmo lo
marca el reloj del multiplexor y no una estimación tuya.

### Que el ritmo lo marque el flujo: `"bitrate": "pcr"`

Acertar el bitrate a mano es un problema real: si le pides 10 Mbps a un TS que
son 6, lo emites 1,6 veces más rápido y le revientas el búfer al decodificador;
si te quedas corto, lo matas de hambre. Y con material de bitrate variable no
hay número correcto.

El propio transport stream lleva la respuesta. El **PCR** (*Program Clock
Reference*) es el reloj del multiplexor, en unidades de 27 MHz, y viaja en el
campo de adaptación de algunos paquetes. Con `pcr` en vez de un número, el
emisor lo lee y reproduce el flujo **exactamente al ritmo al que se creó**:

```json
{ "name": "peli", "dest": "239.0.10.3:5000", "bitrate": "pcr" }
```

Medido sobre un TS cuyo PCR dice 6 Mbps:

| Configuración | Emitido |
|---|---|
| `"bitrate": "20M"` | 12,85 Mbps — lo que le digas, aunque esté mal |
| `"bitrate": "pcr"` | **6,00 Mbps** — el ritmo real del flujo |

Se ancla al reloj del flujo e interpola entre PCR con el ritmo medido entre los
dos últimos, así que no acumula deriva por mucho que dure la emisión. Un salto
del contador —da la vuelta cada ~26 h—, un empalme de material o una
discontinuidad declarada por el propio flujo se detectan y se vuelve a anclar,
en vez de intentar recuperar el desfase de golpe con una ráfaga.

Eso último importa más de lo que parece, y tiene su prueba: la suite para la
fuente 1,5 s en mitad de una emisión de 6 Mbps y mide el **pico** en ventanas de
200 ms. Si al reanudar solo se re-anclara el reloj de bitrate fijo y no el del
PCR, el objetivo lo seguiría calculando el pacer sobre su ancla vieja, cada
vuelta del bucle volvería a verse retrasada y no se dormiría nunca:

| | Pico | `rebase` |
|---|---|---|
| re-anclando solo el reloj de bitrate fijo | 38,8 Mbps | 599 |
| re-anclando los dos | **6,2 Mbps** | **1** |

Si el flujo no trae PCR (no es TS, o no los lleva), tras 4 MB se avisa y se
vuelve al bitrate configurado, que sigue haciendo falta como estimación
inicial: hasta el segundo PCR no hay ritmo medido. Esa estimación sale de
`defaults`, así que en un canal con `"bitrate": "pcr"` conviene que `defaults`
lleve un número por dónde ande el material —arrancar a 10 Mbps un flujo de 50
deja los primeros milisegundos a cámara lenta—.

Exige que `size` sea múltiplo de 188, porque si no los paquetes TS salen
partidos y no hay PCR que leer.

### RTP: `"rtp": true`

Añade la cabecera de 12 bytes de RFC 3550 con *payload type* 33 (MP2T). La
necesitan SMPTE 2022-2 y muchos decodificadores profesionales, y de paso da al
receptor **números de secuencia** para detectar pérdidas y reordenaciones, cosa
imposible con UDP a pelo.

Por eso el payload por defecto son 1316 bytes: 1316 + 12 = 1328, que cabe
holgado en una MTU de 1500. La secuencia y el SSRC arrancan aleatorios, como
manda la norma.

### Sigue sin ser un multiplexor

`mcast-send` trocea, pacea y encapsula. Del transport stream solo lee el campo
de adaptación —lo justo para encontrar el PCR—; **no parsea el contenido**: no
toca las tablas PSI/SI, no reescribe PID ni service IDs, no recodifica y no
multiplexa varios programas en uno.

Lo que **no** hace, y para lo que necesitas otra cosa:

| | |
|---|---|
| FEC (SMPTE 2022-1) o *seamless protection* (2022-7) | un gateway dedicado |
| SRT, RIST, Zixi | `srt-live-transmit`, `ristsender` |
| transcodificar, remultiplexar, reescribir PSI/SI | `ffmpeg`, `tsduck` — con `exec` los tienes delante |
| IPv6, RTCP, *retransmission* | — |

Con `exec` delega el formato en quien sabe hacerlo. Él pone el reloj, la
cabecera RTP y el socket.

El tamaño por defecto es **1316 = 7 × 188**, un número entero de
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
| `name` | canal | el `dest` | nombre en los logs; identifica el canal en las recargas |
| `dest` | canal | obligatorio | `GRUPO:PUERTO` al que emitir |
| `file` | canal | — | fichero a emitir |
| `loop` | canal | true | repetir el fichero al acabarlo |
| `stdin` | canal | false | leer de la entrada estándar |
| `exec` | canal | — | orden externa cuya salida estándar se emite |
| `rtp` | ambos | false | encapsular cada datagrama en RTP |
| `bitrate` | ambos | `10M` | bits/s, con sufijo, o `pcr` para seguir el reloj del flujo |
| `size` | ambos | 1316 | bytes de payload por datagrama |
| `stats` | defaults | 10 | segundos entre resúmenes (0 los apaga) |
| `iface`, `ttl`, `loopback`, `sndbuf` | ambos | | como en el relé |

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
[`internal/mcast/control_linux.go`](internal/mcast/control_linux.go)).

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
- **El join por fuente (SSM) no está en todas partes.** El filtrado por emisor
  con `from` funciona siempre, pero solo se corta el tráfico *en la red* donde
  la plataforma implementa el join por fuente. En Windows `x/net` no lo hace, y
  allí se filtra en el propio relé: protege el flujo igual, pero el tráfico del
  emisor ajeno llega a la NIC y se descarta después. El arranque lo avisa.
- **Una syscall por paquete y destino.** No usa `recvmmsg`/`sendmmsg`. Sobra
  para decenas de canales; si necesitas cientos, el techo está aquí.
- **Linux es la plataforma principal.** Compila y funciona en Windows y
  macOS/BSD, pero en Windows no existe `SIGHUP` (no hay recarga en caliente) ni
  se puede filtrar por dirección de destino (el arranque lo avisa).
- **Lo que las pruebas no alcanzan.** El camino de datos sí está cubierto: la
  suite levanta emisor, relé y receptor sobre sockets de verdad y comprueba que
  el patrón numerado llega entero, sin duplicados, sin huecos y sin mezclarse
  con otro grupo; que con `rtp` los datagramas salen con su cabecera de 12
  bytes, el payload intacto y la secuencia sin saltos; y que un parón de la
  fuente no se convierte en una ráfaga. Lo que **no** se puede probar en CI es
  el **filtrado SSM a nivel de red**: comprobar que el switch no nos manda
  siquiera el tráfico de otros emisores exige IGMPv3 en el camino. Lo que sí se
  verifica es que el join se hace y que el emisor ajeno no llega al destino.

## Licencia

MIT. Ver [LICENSE](LICENSE).

Los binarios son estáticos, así que el ejecutable lleva dentro el runtime de Go
y `golang.org/x/net` y `x/sys`, los tres BSD 3-Clause. Sus avisos están en
[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md), que es lo que esas licencias
piden al redistribuir en forma binaria — actualízalo si cambia `go.mod`.
