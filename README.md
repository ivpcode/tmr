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
| `ls` | | | Elenca le sessioni. |
| `rename` | `rn` | `<vecchio> <nuovo>` | Rinomina una sessione. |
| `to` | | `<nome>` | Sposta il client attivo su un'altra sessione. |

Per staccarsi dall'interno di una sessione si preme **`Ctrl-\`** (la sessione
resta viva). In alternativa, da un altro terminale: `ivt detach <nome>`.

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

Il server (demone) parte da solo al primo `new`/`resume` e si spegne quando
l'ultima sessione viene chiusa. Il socket è `$IVT_SOCK`, altrimenti
`/tmp/ivt-<uid>/default`.

## Architettura (zero dipendenze)

| Package | Ruolo | Cosa sostituisce di tmux |
|---|---|---|
| `internal/server` | demone: possiede lo stato e serve i client | server-client.c |
| `internal/client` | comandi one-shot + attach interattivo (raw mode) | client.c |
| `internal/tmux` | modello sessioni + runtime PTY + ring buffer | session.c |
| `internal/pty` | apertura PTY e avvio processi (syscall Linux) | forkpty/openpty |
| `internal/term` | raw mode e dimensione del terminale (syscall) | termios/terminfo |
| `internal/ipc` | protocollo a frame su socket unix | libevent + imsg |
| `internal/command` | registry + parser + Ctx dei comandi | cmd.c + arguments.c |

Nessun uso di `cgo`. L'event loop di libevent è sostituito da goroutine e
channel; terminfo da un profilo terminale fisso (`xterm-256color`).

## Come funziona l'attach

Il server tiene aperta la PTY di ogni sessione e ne bufferizza l'output in un
ring buffer. Quando ti attacchi (`resume`/`new`), il client mette il terminale
locale in raw mode e apre uno stream con il server: l'output della PTY arriva al
tuo terminale (con replay dello scrollback recente), i tuoi tasti vanno alla
PTY, e i cambi di dimensione (`SIGWINCH`) vengono propagati. Staccandoti, lo
stream si chiude ma il processo continua a girare nel server.

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
