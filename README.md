# ivt — multiplexer di sessioni per agenti, in Go e senza dipendenze

`ivt` è un multiplexer di terminale ispirato a [tmux](https://github.com/tmux/tmux),
ridotto all'osso e **senza dipendenze esterne** (solo la standard library di Go).
Serve un caso d'uso preciso: lanciare e riprendere **agenti a lunga durata** (come
Claude Code) in sessioni che restano vive anche quando ti stacchi dal terminale.

Rispetto a tmux (~103.000 righe di C, 92 comandi, dipendente da libevent +
ncurses/terminfo), `ivt` tiene **solo le sessioni**: niente finestre, pane,
copy-mode, hook o modi interattivi. L'analisi del sorgente originale e il piano
di riduzione sono in [`docs/ANALISI.md`](docs/ANALISI.md).

## Comandi

| Comando | Alias | Argomenti | Descrizione |
|---|---|---|---|
| `new` | `n` | `[nome] [comando [args...]]` | Crea una sessione e vi si attacca. Senza nome ne genera uno progressivo; senza comando avvia la shell. |
| `resume` | `r` | `<nome>` | Si attacca a una sessione esistente. |
| `detach` | `d` | `<nome>` | Stacca i client da una sessione (da un altro terminale). |
| `kill` | | `<nome>` | Distrugge una sessione e il suo processo. |
| `ls` | | `[-j]` | Elenca le sessioni (`-j` per output JSON). |
| `rename` | `rn` | `<vecchio> <nuovo>` | Rinomina una sessione. |
| `to` | | `<nome>` | Sposta il client attivo su un'altra sessione. |
| `web` | | `[-t token] <porta\|host:porta>` | Interfaccia web (HTTPS): lista sessioni + terminale nel browser. |

Per staccarsi dall'interno di una sessione si preme **`Ctrl-\`** (la sessione
resta viva). In alternativa, da un altro terminale: `ivt detach <nome>`.
`ivt help` mostra l'uso senza bisogno del server.

Se il processo della sessione termina mentre sei attaccato, `ivt` esce con **lo
stesso exit code** del processo — utile negli script che lanciano agenti.

## Esempi

```sh
go build -o ivt ./cmd/ivt

ivt n work claude       # crea la sessione "work" che esegue "claude" e vi si attacca
# ... lavori con l'agente ...
# premi Ctrl-\ per staccarti; claude continua a girare

ivt ls                  # work: claude  [5m]
ivt r work              # ti riattacchi (rivedi lo scrollback recente)

ivt n build             # nuova sessione con la shell
ivt to work             # sposta il client attivo sulla sessione "work"
ivt rename work agent   # rinomina
ivt kill agent          # termina la sessione
```

Il server (demone) parte da solo al primo `new` e si spegne quando l'ultima
sessione viene chiusa. Il socket è `$IVT_SOCK`, altrimenti
`/tmp/ivt-<uid>/default`; gli eventuali messaggi diagnostici del demone
finiscono in `server.log` accanto al socket. `kill` termina l'**intero process
group** della sessione, quindi anche i processi figli lanciati dalla shell.

## Interfaccia web

```sh
ivt web 9000                  # tutte le interfacce (0.0.0.0:9000)
ivt web 127.0.0.1:9000        # solo localhost
ivt web 192.168.1.234:9000    # una interfaccia specifica
ivt web -t miotoken 9000      # token fisso invece di quello generato
```

L'indirizzo è **obbligatorio** (nessuna porta di default): un numero da solo
significa "quella porta su tutte le interfacce", `host:porta` limita l'ascolto
a quell'indirizzo. All'avvio vengono stampati gli URL pronti da aprire.

Il gateway mostra la **lista delle sessioni attive** (auto-aggiornata, con età
e client attaccati) e, cliccando su una sessione, apre un **terminale completo
nel browser** ([xterm.js](https://xtermjs.org), incorporato nel binario): si
lavora nella stessa identica sessione della CLI, con replay dello scrollback,
resize automatico e creazione/kill delle sessioni dalla pagina. Chiudere la
scheda equivale a un detach: la sessione continua.

**Il traffico è sempre HTTPS** (WebSocket compreso: `wss`), mai HTTP in chiaro:
alla prima esecuzione viene generato un certificato **autofirmato** ECDSA
valido 10 anni, persistito in `~/.config/ivt/` — il browser chiede conferma
solo al primo accesso e l'eccezione resta valida ai riavvii. L'accesso
richiede sempre il **token** stampato all'avvio (query `?t=` la prima volta,
poi cookie `Secure`): senza token ogni richiesta è respinta, anche da
localhost.

Il gateway è un normale client del demone: fa da ponte tra WebSocket (RFC 6455
implementato su stdlib, `internal/ws`) e il protocollo IPC. CLI e browser
possono essere attaccati alla stessa sessione contemporaneamente.

## Architettura (zero dipendenze)

| Package | Ruolo | Cosa sostituisce di tmux |
|---|---|---|
| `internal/server` | demone: possiede lo stato e serve i client | server-client.c |
| `internal/client` | comandi one-shot + attach interattivo (raw mode) | client.c |
| `internal/tmux` | modello sessioni + runtime PTY + ring buffer | session.c |
| `internal/pty` | apertura PTY e avvio processi (syscall Linux) | forkpty/openpty |
| `internal/term` | raw mode e dimensione del terminale (syscall) | termios/terminfo |
| `internal/ipc` | protocollo binario a frame su socket unix | libevent + imsg |
| `internal/command` | registry + parser + Ctx dei comandi | cmd.c + arguments.c |
| `internal/ws` | server WebSocket minimale (RFC 6455) | — |
| `internal/web` | gateway HTTP: lista sessioni + terminale browser | — |

Nessun uso di `cgo` e nessun modulo Go esterno. L'event loop di libevent è
sostituito da goroutine e channel; terminfo da un profilo terminale fisso
(`xterm-256color`). L'unico codice di terzi è **xterm.js** (MIT), incorporato
nel binario con `go:embed` per il terminale web — nessun CDN a runtime.

## Come funziona l'attach

Il server tiene aperta la PTY di ogni sessione e ne bufferizza l'output in un
ring buffer. Quando ti attacchi (`resume`/`new`), il client mette il terminale
locale in raw mode e apre uno stream con il server: l'output della PTY arriva al
tuo terminale (con replay dello scrollback recente), i tuoi tasti vanno alla
PTY, e i cambi di dimensione (`SIGWINCH`) vengono propagati. Staccandoti, lo
stream si chiude ma il processo continua a girare nel server.

I frame sul socket sono binari — `[1 byte tipo][4 byte lunghezza][payload]` —
con l'output del terminale trasportato **raw** (5 byte di overhead per chunk,
una sola write per frame, ~1,1 GB/s in round-trip nel benchmark); il JSON è
usato solo per i frame di comando/risposta. Un client che smette di leggere
viene scollegato d'ufficio invece di bloccare l'output della sessione per gli
altri; al riattacco il ring buffer ripristina lo schermo recente.

## Il sistema di comandi (semplificazione rispetto a tmux)

In tmux ogni comando è un file separato con la spec argomenti in una stringa
getopt criptica (`"af:t:"`). In `ivt` un comando è una struct dichiarativa;
i comandi sono posizionali e raggruppati in un unico file:

```go
var killSession = &command.Command{
    Name:    "kill",
    Summary: "destroy a session",
    MinArgs: 1, MaxArgs: 1,
    Run: func(c *command.Ctx) error {
        return c.Server.Kill(c.Args[0])
    },
}
```

## Sviluppo

```sh
go build ./...
go test ./...
go vet ./...
```

Solo Linux (usa PTY e termios via `syscall`).
