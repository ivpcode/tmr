# Analisi di tmux e piano per il clone in Go (zero dipendenze)

> Documento di analisi preliminare. Nessun codice scritto prima di condividere questo piano.
> Fonte analizzata: `https://github.com/tmux/tmux.git` (clone completo).

## 1. Dimensioni del sorgente originale

| Metrica | Valore |
|---|---|
| File `.c`/`.h` | 157 |
| Righe di C totali | ~103.200 |
| File comando (`cmd-*.c`) | 63 |
| Voci di comando (`cmd_entry`, inclusi alias) | ~100 |
| Comandi copy-mode | 98 |
| Variabili format (`#{...}`) | 205 |
| Opzioni (`options-table.c`) | 185 |
| Key binding di default | 305 |

Solo i moduli "riducibili" (copy-mode, format, opzioni, modi interattivi,
control mode, terminfo) valgono da soli **~30.500 righe**: qui si gioca gran
parte della riduzione.

## 2. Architettura di tmux (a strati)

```
+---------------------------------------------------------------+
|  CLIENT (tmux attach)         SERVER (demone singolo)         |
|  - legge tastiera             - possiede tutto lo stato        |
|  - disegna sul terminale      - session > window > pane        |
|         \___ IPC socket unix (imsg) ___/                       |
+---------------------------------------------------------------+
        libevent (event loop)         ncurses/terminfo (capacità terminale)
```

Strati logici:
1. **Client/Server + IPC** — un demone possiede lo stato; i client si
   attaccano via socket unix. Framing messaggi con `imsg`.
2. **Event loop** — `libevent`: fd, timer, segnali.
3. **Modello dati** — `session` → `winlink` → `window` → `window_pane`;
   ogni pane ha una `grid` (buffer di celle) e uno `screen`.
4. **PTY + processi** — ogni pane esegue una shell in uno pseudo-terminale.
5. **Parser input terminale** — `input.c` (macchina a stati VT100/ANSI, 3691 righe).
6. **Rendering** — `screen-write.c`, `screen-redraw.c`, `tty-draw.c`,
   `format-draw.c`; scrittura sul terminale reale via `tty.c` + terminfo.
7. **Comandi** — ~100 comandi, parser (`cmd-parse.y`, yacc), coda
   (`cmd-queue.c`), risoluzione target (`cmd-find.c`), argomenti (`arguments.c`).
8. **Format language** — `#{...}` con 205 variabili + funzioni.
9. **Opzioni** — 185 opzioni server/session/window.
10. **Modi interattivi** — copy-mode, choose-tree, customize, clock (motore
    comune `mode-tree.c`).
11. **Layout** — algoritmi di suddivisione dei pane.
12. **Extra** — hooks, control mode (`tmux -CC`), notify.

## 3. Dipendenze esterne da eliminare (obiettivo: solo stdlib Go)

| Dipendenza C | Uso in tmux | Sostituzione in Go (stdlib) |
|---|---|---|
| **libevent** | event loop (50 file) | goroutine + channel + `net`/`os` |
| **ncurses/terminfo** | capacità terminale (11 file) | **profilo terminale fisso** (xterm-256color / truecolor), sequenze ANSI hard-coded |
| **imsg** | IPC client/server | `net.UnixConn` + framing custom (length-prefixed) |
| **`<sys/queue.h>` (TAILQ)** | liste concatenate ovunque | slice/map/`container/list` |
| **yacc (`cmd-parse.y`)** | parser comandi | parser ricorsivo scritto a mano |
| **getopt / getopt_long** | parsing flag | parser argomenti custom |
| **forkpty / openpty** | PTY | `syscall` (Linux) — chiamate dirette, niente cgo |

> La sostituzione più impattante è **terminfo → profilo fisso**: tmux supporta
> centinaia di terminali storici. Un clone moderno può assumere un terminale
> ANSI/xterm-256color e cancellare `tty-term.c`, `tty-features.c`, `tty-acs.c`
> e gran parte di `tty-keys.c`.

## 4. ELENCO delle funzioni/moduli da RIDURRE o TAGLIARE

### 4a. Da TAGLIARE completamente nel MVP (~13.000 righe)

| Modulo | Righe | Perché si taglia |
|---|---:|---|
| `control.c` + `control-notify.c` | ~1.150 | Control mode (`tmux -CC`, integrazione iTerm2). Non essenziale. |
| `window-customize.c` | 2.882 | UI interattiva per modificare opzioni. |
| `window-tree.c` | 1.561 | UI `choose-tree`/`choose-session`. |
| `mode-tree.c` | 1.836 | Motore comune dei menu interattivi ad albero. |
| `window-client.c`, `window-buffer.c`, `window-clock.c`, `window-visible.c`, `window-switch.c`, `window-border.c` | ~2.500 | Modi interattivi vari (choose-client, clock-mode, ecc.). |
| `hooks.c` | ~600 | Sistema di hook sugli eventi. |
| `layout-set.c`, `layout-custom.c` | ~1.000 | Layout predefiniti + (de)serializzazione stringa layout. |

### 4b. Da RIDURRE drasticamente

| Modulo | Righe C | Target Go | Cosa si tiene |
|---|---:|---:|---|
| `format.c` | 6.894 | ~500 | Da **205 variabili** a ~30 essenziali (`#{session_name}`, `#{window_index}`, `#{pane_id}`, `#{host}`, `#{pane_title}`…). Niente modificatori esoterici. |
| `window-copy.c` | 7.165 | ~800 | Da **98 comandi** a ~15: scroll su/giù/pagina, inizio/fine selezione, copia, ricerca base. Niente rettangolare/incrementale/vi-mark avanzati. |
| `options-table.c` + `options.c` | 3.500 | ~600 | Da **185 opzioni** a ~30 (prefix, status, mode-keys, base-index, history-limit, default-terminal…). |
| `key-bindings.c` | 791 | ~250 | Da **305 bind** a ~40 essenziali (prefix + tasti principali). |
| `tty-term.c`+`tty-features.c`+`tty-acs.c`+`tty-keys.c` | ~4.900 | ~600 | Profilo terminale fisso: sequenze ANSI note, tabella tasti fissa. |
| `input.c` (parser VT) | 3.691 | ~1.500 | Mantenere il parser (serve!) ma potare sequenze DEC/rare. |
| `cmd-find.c` | 1.345 | ~400 | Risoluzione target semplificata (vedi §5). |

### 4c. Da MANTENERE (il cuore, non riducibile)

`grid.c`, `screen.c`, `screen-write.c`, `screen-redraw.c`, `window.c`,
`session.c`, `layout.c` (base), `tty.c` (output), `utf8.c`, `input.c` (parser),
il client/server e la PTY. Questi sono la sostanza di un multiplexer.

### 4d. Riepilogo riduzione stimata

| | C originale | Go stimato |
|---|---:|---:|
| Totale | ~103.000 | **~12.000–15.000** |

## 5. Semplificare come sono fatti i comandi

### Com'è oggi in tmux (C)

Ogni comando è un file separato con parecchio boilerplate. La spec degli
argomenti è una **stringa getopt criptica**:

```c
/* cmd-kill-pane.c — un intero file per un comando */
const struct cmd_entry cmd_kill_pane_entry = {
    .name  = "kill-pane",
    .alias = "killp",
    .args  = { "af:t:", 0, 0, NULL },   // <-- "af:t:" = -a bool, -f arg, -t arg
    .usage = "[-a] [-f filter] " CMD_TARGET_PANE_USAGE,
    .target = { 't', CMD_FIND_PANE, 0 },
    .flags = CMD_AFTERHOOK,
    .exec  = cmd_kill_pane_exec
};
static enum cmd_retval
cmd_kill_pane_exec(struct cmd *self, struct cmdq_item *item) {
    struct args *args = cmd_get_args(self);
    struct cmd_find_state *target = cmdq_get_target(item);
    struct window_pane *wp = target->wp;
    if (args_has(args, 'a')) ...
}
```

Problemi: 1 file per comando, spec `"af:t:"` illeggibile, target risolto con
macro/flag, tanto codice ripetuto (`cmd_get_args`, `cmdq_get_target`…).

### Proposta per il clone (Go): dichiarativo e compatto

**Un registry unico**, spec dei flag **strutturata** (niente `"af:t:"`),
comandi correlati **raggruppati** in un file, target risolto da un helper.

```go
// cmd/kill.go — più comandi correlati nello stesso file
var killPane = &Command{
    Name:    "kill-pane",
    Alias:   "killp",
    Flags: Flags{
        "a": Bool{"kill all but current"},
        "f": Str {"filter"},
        "t": Target{Pane},          // risoluzione target dichiarata qui
    },
    Run: func(c *Ctx) error {
        if c.Bool("a") { return c.KillOtherPanes(c.Flag("f")) }
        return c.Server.KillPane(c.Target.Pane)
    },
}

func init() { Register(killPane, killWindow, killSession, killServer) }
```

Vantaggi:
- **Spec leggibile**: `"a": Bool{...}` invece di decifrare `"af:t:"`.
- **Target dichiarativo**: `Target{Pane}` risolve `-t` in automatico e riempie `c.Target`.
- **Meno boilerplate**: niente forward-declaration, niente `cmd_get_args`; `Run(c *Ctx)` riceve già tutto.
- **File raggruppati per tema** (`kill.go`, `window.go`, `session.go`, `options.go`) invece di 63 file.
- **Un solo dispatcher** con validazione flag + usage generata automaticamente.

## 6. Scope finale: `ivt`, solo sessioni per agenti

Dopo l'analisi, lo scope è stato ristretto a un caso d'uso preciso: **lanciare e
riprendere agenti a lunga durata** (es. Claude Code). Il clone si chiama `ivt` e
tiene **solo la gestione delle sessioni**. Finestre, pane, layout, copy-mode,
modi interattivi, hook, control mode, format language e la quasi totalità delle
opzioni sono **tagliati**.

**Comandi (7, ad argomenti posizionali):**

| Comando | Alias | Argomenti | Descrizione |
|---|---|---|---|
| `new` | `n` | `[nome] [comando [args...]]` | Crea una sessione e vi si attacca |
| `resume` | `r` | `<nome>` | Si attacca a una sessione esistente |
| `detach` | `d` | `<nome>` | Stacca i client da una sessione |
| `kill` | | `<nome>` | Distrugge una sessione |
| `ls` | | | Elenca le sessioni |
| `rename` | `rn` | `<vecchio> <nuovo>` | Rinomina una sessione |
| `to` | | `<nome>` | Sposta il client attivo su un'altra sessione |

**Funzionalità:** server/client su socket unix, sessioni con un processo reale in
PTY che sopravvive al detach, ring buffer per il replay dello scrollback,
attach interattivo in raw mode, resize (SIGWINCH), detach con `Ctrl-\`,
auto-avvio e auto-spegnimento del server.

## 7. Struttura package Go (implementata)

```
tmr/                        (modulo github.com/ivpcode/tmr, binario "ivt")
├── cmd/ivt/main.go         # entrypoint: client o server, dispatch comandi
├── internal/
│   ├── server/             # demone: possiede lo stato, serve i client
│   ├── client/             # comandi one-shot + attach interattivo (raw mode)
│   ├── ipc/                # protocollo a frame su socket unix
│   ├── tmux/               # modello sessioni + runtime PTY + ring buffer
│   ├── pty/                # apertura PTY + avvio processi (syscall Linux)
│   ├── term/               # raw mode + dimensione terminale (syscall)
│   └── command/            # registry + parser + Ctx dei comandi
└── docs/ANALISI.md
```

> Nota: le sezioni 1–5 restano lo studio del sorgente tmux e valgono a
> prescindere dallo scope finale; grid/vt/tty/layout/options non servono nel
> caso d'uso "solo sessioni" perché è il processo nella PTY a gestire il proprio
> rendering — `ivt` fa solo da tramite trasparente.
