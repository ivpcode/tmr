# tmr — un clone di tmux in Go, senza dipendenze

`tmr` è una reimplementazione **ridotta** e **senza dipendenze esterne** (solo
la standard library di Go) del multiplexer di terminale [tmux](https://github.com/tmux/tmux).

L'obiettivo non è la parità 1:1 con tmux (~103.000 righe di C), ma un clone
moderno e mantenibile: ~12–15k righe, un solo terminale target (ANSI /
xterm-256color) e un sistema di comandi semplificato.

L'analisi completa del sorgente originale e il piano di riduzione sono in
[`docs/ANALISI.md`](docs/ANALISI.md).

## Stato attuale: scheletro architetturale

Questo commit contiene lo **scheletro** funzionante end-to-end:

- **Client/server** su socket unix (`internal/server`, `internal/client`)
- **IPC** con framing length-prefixed JSON, al posto di `imsg` (`internal/ipc`)
- **Modello dati** session → window → pane (`internal/tmux`)
- **Sistema di comandi semplificato** (`internal/command`) — vedi sotto
- **Risoluzione target** `-t` in ~150 righe, al posto delle 1.300 di `cmd-find.c`
- Avvio automatico del server in background dal client
- Comandi dimostrativi: `new-session`, `kill-session`, `list-sessions`,
  `has-session`, `list-windows`, `list-panes`, `kill-server`, `list-commands`

Non ancora presenti (prossime fasi): PTY + shell reale, parser ANSI, rendering,
status bar, attach interattivo, copy-mode.

## Il sistema di comandi (semplificazione rispetto a tmux)

In tmux ogni comando è un file separato e la spec degli argomenti è una stringa
getopt criptica (`"af:t:"`). In `tmr` un comando è una struct dichiarativa,
i comandi correlati stanno nello stesso file, e un unico dispatcher gestisce
parsing, risoluzione target, generazione dell'usage ed esecuzione.

```go
var killSession = &command.Command{
    Name:    "kill-session",
    Summary: "destroy a session",
    Flags: command.Flags{
        "t": command.Target(tmux.KindSession), // -t risolto in automatico
    },
    Run: func(c *command.Ctx) error {
        return c.Server.KillSession(c.Target.Session.Name)
    },
}
```

- Spec dei flag **leggibile**: `"a": Bool(...)`, `"s": Str(...)`, `"t": Target(...)`.
- **Nessun boilerplate**: `Run(c *Ctx)` riceve già flag, target e server.
- **Usage generato** automaticamente dalla spec (niente stringhe da mantenere a mano).

## Uso

```sh
go build -o tmr ./cmd/tmr

./tmr new-session -d -s work    # crea una sessione (il server parte da solo)
./tmr list-sessions
./tmr list-windows -t work
./tmr kill-server
```

Il socket è `$TMR_SOCK` oppure `/tmp/tmr-<uid>/default`.

## Sviluppo

```sh
go build ./...
go test ./...
go vet ./...
```

## Layout dei package

```
cmd/tmr/            entrypoint (client o server)
internal/command/   sistema comandi: registry, parser, usage, Ctx
internal/command/cmds/  comandi built-in raggruppati per tema
internal/tmux/      modello dati + risoluzione target
internal/ipc/       protocollo client/server (socket unix)
internal/server/    demone
internal/client/    invio comandi al demone
docs/ANALISI.md     analisi di tmux e piano di riduzione
```
