# mcast-dup

**Español** · [English](README.en.md)

[![CI](https://github.com/eFeSpain/mcast-dup/actions/workflows/ci.yml/badge.svg)](https://github.com/eFeSpain/mcast-dup/actions/workflows/ci.yml)

Duplica flujos multicast UDP: recibe un grupo y reenvía cada datagrama tal cual
a uno o varios grupos distintos. Sin recodificar y sin tocar el payload.

Un binario estático sin dependencias en tiempo de ejecución, N canales en un
solo proceso y recarga en caliente con SIGHUP.

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
CGO_ENABLED=0 go build -o mcast-dup .

# cruzado, para un servidor Linux
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mcast-dup .
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
| `-iface` | auto | IP local de la NIC que se usa para recibir y emitir |
| `-ttl` | 8 | TTL multicast de salida |
| `-loop` | true | loopback multicast local |
| `-rcvbuf` | 4 MiB | `SO_RCVBUF` del socket de recepción |
| `-sndbuf` | 0 | `SO_SNDBUF` (0 = no tocarlo) |
| `-stats` | 10 | segundos entre resúmenes (0 los apaga) |
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
| `iface` | ambos | auto | IP local de la NIC (rx y tx) |
| `ttl` | ambos | 8 | TTL multicast de salida |
| `loop` | ambos | true | loopback multicast local |
| `rcvbuf` | ambos | 4194304 | `SO_RCVBUF` en bytes |
| `sndbuf` | ambos | 0 | `SO_SNDBUF` en bytes (0 = no tocarlo) |
| `stats` | defaults | 10 | segundos entre resúmenes (0 los apaga) |
| `name` | canal | `ch1`, `ch2`… | nombre en los logs; identifica el canal en las recargas |
| `source` | canal | obligatorio | `GRUPO:PUERTO` de origen, tiene que ser multicast |
| `dest` | canal | obligatorio | lista de `GRUPO:PUERTO` destino (también vale unicast) |

### Qué se valida, al arrancar y en cada recarga

- El origen tiene que ser una dirección multicast.
- Se rechaza cualquier canal que **cree un bucle de realimentación**: un
  destino que vuelva, directa o indirectamente, al origen del propio canal.
  Con el loopback activado eso multiplica el flujo en cada vuelta hasta saturar
  la NIC. Una cascada legítima (el canal A alimenta el grupo que lee el canal
  B) sí se permite.
- Los canales con nombre duplicado o direcciones inválidas se descartan con un
  aviso, sin tumbar a los demás.

En modo flags cualquier problema es fatal y el proceso sale con código 2. En
modo daemon se ignora el canal afectado y el resto sigue.

### Recarga

`SIGHUP` arranca los canales nuevos, para los que desaparecen y reinicia solo
los que han cambiado, esperando a que el canal viejo cierre sus sockets antes
de arrancar el relevo. Un JSON inválido no tumba nada: se avisa por log y sigue
en pie la configuración anterior.

## systemd

```sh
install -m 0755 mcast-dup /usr/local/bin/
install -m 0644 mcast-dup.example.json /etc/mcast-dup.json
install -m 0644 mcast-dup.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now mcast-dup
systemctl reload mcast-dup        # tras editar /etc/mcast-dup.json
```

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

## Limitaciones

- **Solo IPv4.**
- **Sin SSM ni filtrado por fuente** (IGMPv3): no se puede pedir «este grupo,
  pero solo del emisor X».
- **Una syscall por paquete y destino.** No usa `recvmmsg`/`sendmmsg`. Sobra
  para decenas de canales; si necesitas cientos, el techo está aquí.
- **Linux es la plataforma principal.** Compila y funciona en Windows y
  macOS/BSD, pero en Windows no existe `SIGHUP`: no hay recarga en caliente.
- Los tests automáticos cubren validación, filtrado por grupo, parada de
  canales, estadísticas y traducciones. El camino de datos en sí está
  verificado a mano.

## Licencia

MIT. Ver [LICENSE](LICENSE).
