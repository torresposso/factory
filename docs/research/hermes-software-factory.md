# Hermes como fábrica de software

Investigación del 2026-10-24. Tres partes leídas por separado y juntadas aquí: el kanban de Hermes, el
resto de Hermes (perfiles, skills, delegación, cron, webhooks, pares, seguridad, costo) y cómo funciona hoy
la fábrica en Basecamp.

**Fuentes.** Todo sale de fuentes primarias: el código de
[NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent) en el tag `v0.22.5`
(`bf0203a794`) comparado con `v0.21.6` (`818c13be1d`), su documentación (`website/docs/`, publicada en
<https://hermes-agent.nousresearch.com/docs/>) y las notas de cada release en GitHub. Lo de Basecamp sale de
comandos de solo lectura en el contenedor `hermes`. Las rutas `docs:` son relativas a `website/docs/`, y las
de código, al repo de Hermes. Lo que no se probó corriendo algo está marcado **sin verificar**.

## 1. Lo esencial

1. **El diseño es el que recomienda Hermes.** Su documentación tiene como caso de uso del kanban
   «decompose → implement in parallel worktrees → review → iterate → PR» (`docs: user-guide/features/kanban.md:43`).
   Para Hermes los roles no son más que perfiles con su nombre, y no trae una lista fija de ellos
   (`docs: kanban-worker-lanes.md`). Los traspasos entre roles van en tarjetas, que sobreviven a un reinicio y
   quedan registradas. `delegate_task` sirve solo para lecturas cortas en paralelo dentro de una tarjeta
   (`docs: kanban.md:97-122`).
2. **Basecamp va dos versiones menores atrás.** Tiene `v0.21.6` y la última es `v0.22.5`, del 2026-10-23
   ([release](https://github.com/NousResearch/hermes-agent/releases/tag/v0.22.5)). La CLI del kanban y su
   modelo de datos casi no cambiaron, así que actualizar no rompe a `factory`. Lo que se gana es robustez:
   ya no quedan tarjetas pegadas en `running` cuando un worker muere en un reinicio (commit `9dae8d87ef`),
   y los workers sí registran shell hooks (commit `fcc895496e`).
3. **El webhook todavía no usa el programa.** Las rutas `github-torresposso-site` y `github-factory-sandbox`
   siguen con el prompt viejo de unas 140 líneas, en el que el perfil `orchestrator` corre git, gh y kanban él
   mismo. `factory plan` solo se ha corrido a mano, en el tablero `laiacaliente-app`. Eso deja dos fuentes
   de verdad, que ya se separaron: la ruta en prosa no crea tarjeta de Review cuando el ticket va solo, y su
   publisher escribe él mismo el cuerpo del PR. **Resuelto el 2026-10-24 quitando el webhook:** las dos
   rutas, el Funnel `/webhooks`, el puerto `8644` y el hook de GitHub ya no existen. Quien publica el spec
   le da la orden a Hermes (`factory plan --issue N` desde el repo, por chat o por `hermes peer run`).
4. **Hay una afirmación de `docs/hermes.md` que el código contradice.** El archivo dice que
   `kanban_task_completed` solo le llega a un plugin de Python. Pero ese evento está en `VALID_HOOKS`
   (`hermes_cli/plugins.py:161`), que es la lista de eventos permitidos para shell hooks
   (`agent/shell_hooks.py:372-391`) y para webhooks de salida (`agent/outbound_webhooks.py:163-184`). En
   `v0.22.5` el worker registra sus hooks, y una tarjeta siempre los acepta
   (`gateway/session_policy.py:775-789`, `gateway/session_kanban.py:65`). En `0.21.6` el worker no los
   registraba, así que lo que se observó entonces era cierto. **Probado en Basecamp el 2026-10-24, ya en
   `0.22.5`:** el hook del perfil asignado corrió en el worker sin estar aprobado. Recibió `task_id`,
   `board`, `assignee`, `run_id` y `summary`, pero no la metadata de la corrida.
5. **El modelo es la palanca de costo.** Hoy los cinco perfiles usan `deepseek-v4.1-flash`. La documentación
   recomienda un modelo de punta para el que planea o revisa y uno barato para los workers, porque ahí
   se va la mayoría de los tokens (`docs: kanban.md:685-703`). Hermes no trae un tope nativo de dólares ni de
   tokens por perfil o por tarjeta (**sin verificar**). Lo que sí trae son límites de turnos, de tiempo y de
   reintentos.

## 2. Cómo encajan las piezas de Hermes

```
spec + tickets publicados en GitHub
      │  él le da la orden a Hermes (chat o hermes peer run basecamp)
      ▼
                     cd <repo> && factory plan --issue N
                                ▼
                    kanban (un tablero por repo)
   coder ×T ──▶ Integrate ──▶ Review (qa) ──▶ Publish (publisher: factory publish)
      ▲ despachador del gateway: claim → hermes -p <assignee> chat -q, en el workspace dir:
                                ▼
                       un pull request → lo mergea él
```

Así hoy no funciona: es la forma de armarlo con lo que trae Hermes. Lo que cita cada paso está en las
secciones 3 y 4.

## 3. El kanban (lo que usa `factory`)

### Lo que dice `docs/hermes.md`, contra `v0.22.5`

| Dato | Resultado | Fuente |
|---|---|---|
| Pasar siempre `--board`; nunca `boards switch` | Confirmado. Si el tablero no existe, `--board` falla, no lo crea. `HERMES_KANBAN_BOARD` hace lo mismo | `hermes_cli/kanban_db.py:422-463`, `kanban.py:160-179` |
| `list --json` trae `body` pero no `parents` ni `idempotency_key` | Confirmado, con un ajuste: **`show --json` tampoco trae la llave de idempotencia**. Solo está en la base de datos, así que reconocer las tarjetas por título y `workspace_path` sigue siendo la única vía | `hermes_cli/kanban_output.py:16-22` |
| Los padres tienen que existir antes que los hijos | Confirmado | `kanban_db_graph.py:42` |
| Archivar un padre libera a los hijos | Confirmado. Un hijo creado bajo un padre archivado nace en `todo` y pasa a `ready` en el siguiente `recompute_ready` (en cada tick y en cada `list`) | `kanban_db.py:2035-2099`, `:3788`; `kanban_db_graph.py:49` |
| La llave de idempotencia devuelve la tarjeta si no está archivada | Confirmado. Si la encuentra, devuelve la tarjeta tal cual, sin cambiarle el cuerpo ni los padres. Dos creaciones al mismo tiempo pueden duplicarla | `kanban_db.py:1224-1233` |
| `--completion-contract OWNER/REPO` falla si el repo no tiene checks obligatorios | Confirmado, pero falla **al completar**, no al crear | `hermes_cli/kanban_pr_acceptance.py:21-25`, `:145` |
| `kanban_task_completed` solo le llega a Python | **Contradicho** (ver la sección 1, punto 4) | `plugins.py:161`, `managed_worker.py:348-350` |
| `verification`, `branch`, `commit` y `changed_files` van en la metadata | Confirmado como convención: la metadata es JSON libre. `branch` y `commit` los puso la fábrica, Hermes no los define | `kanban_db.py:2639-2644`; `docs: kanban.md` ~527-560 |

Otros hechos que sirven para el diseño:

- **El workspace `dir:`** tiene que ser una ruta absoluta y nunca se borra, ni al completar ni al archivar.
  Eso le da la razón a `factory`, que maneja sus propios worktrees (`kanban_db_workspace.py:362-423`,
  `_REMOVABLE_KINDS`). Con `worktree:` sería Hermes el que crea `<repo>/.worktrees/<id>` y lo borra.
- **El traspaso entre tarjetas**: el worker de un hijo ve el `summary` y la `metadata` de la última corrida
  de cada padre (`docs: kanban.md` ~1160-1190). La rama y el commit le llegan solos a la tarjeta siguiente.
- **Los fallos**: después de 2 fallos seguidos la tarjeta se bloquea (`DEFAULT_FAILURE_LIMIT = 2`,
  `kanban_db_dispatch.py:37`), y lo mismo pasa con 3 violaciones de protocolo, es decir, terminar sin
  `kanban_complete` ni `kanban_block` (`:938`). El claim dura 900 s (`kanban_db.py:279`). `--max-retries`
  cambia el límite para una tarjeta.
- **Los guardias de reintento**: `active_pr` y `recent_success` pueden dejar quieta en `ready`, sin avisar, una
  tarjeta de Publish cuya corrida anterior ya comentó la URL del PR. `hermes kanban dispatch --dry-run --json`
  dice por qué está quieta (`docs: kanban.md` ~1035-1041).
- **La concurrencia**: si `kanban.max_in_progress` no está fijado, no es ilimitado, como dice la
  documentación. El código calcula `clamp(MemTotal/512MB, 2, 8)`, que en Basecamp da 8
  (`kanban_db_dispatch.py:1834-1885`). En Basecamp está fijado en `3`, en el `config.yaml` del perfil
  `default`. **Sin verificar:** de qué perfil lee esa configuración el gateway multiplexado.

### Lo que `factory` podría usar y no usa

1. **Un hook o webhook de salida en `kanban_task_completed` y `kanban_task_blocked`**, en vez de una tarjeta
   Publish que depende de que un modelo barato corra un solo comando. Hay que probarlo primero. Los hooks
   solo observan: si fallan, la tarjeta sigue igual.
2. **`--max-retries 1` en la tarjeta Publish**: un reintento podría intentar abrir el PR otra vez.
3. **`show --json`, que ya trae `runs[]` con `summary` y `metadata`** (`kanban.py:495-503`). Con eso sobra
   la llamada aparte a `runs` en `publish`.
4. **`hermes kanban diagnostics --json`** (`stranded_in_ready`, `stale_running`,
   `running_with_open_parents`), para responder «por qué está parada la corrida».
5. **La revisión sobre la misma tarjeta** (`kanban_request_review` y `kanban_request_changes`): el revisor le
   devuelve la tarjeta al coder sin contarlo como fallo. Achicaría el grafo, pero cambia el modelo de corridas
   y metadata. Por ahora no conviene: la tarjeta Review aparte es más fácil de auditar.
6. **`OWNER/REPO` con `metadata.published_pr`**, solo en los repos que tengan checks obligatorios. Ahí
   Hermes revisaría el CI del commit exacto del PR. Para los demás, `local-only` sigue siendo lo correcto.

Ojo con lo que dice la documentación y el código no tiene: `create --scheduled-at` no existe en el parser, y
`swarm --workers a,b,c` en realidad se escribe `--worker PROFILE:TITLE` repetido. Manda el código.

## 4. Perfiles, skills, delegación, cron, webhooks y pares

**Perfiles** (`docs: user-guide/profiles.md`).

- Cada perfil es un `HERMES_HOME` propio: `config.yaml`, `.env`, `SOUL.md`, memoria, skills, sesiones, cron
  y base de estado.
- **No es un sandbox**: con el backend `local` el agente ve todo lo que ve el usuario del sistema.
- El despachador corre `hermes -p <assignee> chat -q` en el workspace de la tarjeta
  (`hermes_cli/kanban_db_dispatch.py:2755`).
- Si el assignee no corresponde a ningún perfil, la tarjeta se queda en `ready` con un evento
  `skipped_nonspawnable`.
- `--description` en cada perfil ayuda a repartir las tarjetas.
- `GH_TOKEN` va en el `.env` del perfil: una variable exportada en el shell se descarta
  (`docs: kanban.md:68-78`).

**El modelo de cada perfil.**

- Se elige con `model.default` en el `config.yaml` del perfil.
- Para una tarjeta suelta: `kanban create --model X --provider Y` o `kanban set-model <id>`
  (`docs: kanban.md:669-683`).
- `agent.max_turns` no tiene límite por defecto (`docs: user-guide/configuration.md:1266-1310`). En
  Basecamp está en 500.

**Memoria y skills.**

- La memoria es de cada perfil y es chica: `MEMORY.md` hasta 2.200 caracteres y `USER.md` hasta 1.375
  (`docs: features/memory.md:12-20`).
- Las skills también son de cada perfil, salvo `skills.external_dirs`, que Basecamp ya usa con
  `/opt/data/shared-skills`.
- **Las skills propias de un repo** van en `<repo>/.hermes/skills/` y tienen prioridad sobre las demás.
  Piden `hermes skills trust` una vez por repo (`docs: features/skills.md:467-519`). Es el lugar natural
  para las convenciones de cada repo.
- `skills.write_approval: true` deja en espera, para que él las revise, las skills que escriba un agente.
- El Curator archiva las skills de agente que nadie usa: quedan viejas a los 14 días y se archivan a los 30
  (`docs: features/curator.md`).

**La delegación** (`docs: features/delegation.md`).

- Por defecto: hasta 10 hijos en paralelo, sin anidar, 250 iteraciones y sin límite de tiempo.
- Todos los hijos usan el mismo `delegation.model`.
- Los hijos no tienen memoria, ni `clarify`, ni `cronjob`, y no se retoman después de un reinicio.
- Con 2 núcleos conviene bajar `max_concurrent_children`.
- Desde `v0.22.0` funciona dentro de una tarjeta (commit `bf2b3713ab`).
- `v0.22.5` trae backends de delegación que se pueden cambiar: por mapa de modelos o por perfil.

**Cron** (`docs: features/cron.md`).

- Un job carga el `AGENTS.md` del repo solo si tiene `--workdir` (líneas 181-205).
- `--no-agent` **pide un archivo de script** dentro de `$HERMES_HOME/scripts/` (línea ~930). Eso choca con
  la regla de «nada de scripts propios».
- La alternativa sin script es un job con modelo que conteste solo `[SILENT]` cuando todo está bien. Cuesta
  un turno de modelo por tick.

**Webhooks** (`docs: messaging/webhooks.md`).

- El toolset por defecto no tiene terminal (líneas 546-585). Ampliarlo se hace a mano en el config.
- Una ruta con `cron_job:` dispara un job ya existente, con su workdir, su modelo y sus skills, y responde 202
  (líneas 447-492). Es la forma documentada de pasar de «evento de GitHub» a «corrida dentro del repo con su
  `AGENTS.md`».
- `coalesce` junta en una sola corrida varios eventos que llegan seguidos.
- **El texto del issue no es confiable aunque la firma HMAC sea válida** (líneas 642-655).
- No encontré una ruta nativa de webhook que cree tarjetas (**sin verificar**).

**Pares.**

- `hermes peer dm` espera la respuesta y sirve para mensajes cortos.
- `hermes peer run` devuelve un `run_id`, que se consulta con `peer status`. `--idempotency-key` evita que
  un reintento duplique el trabajo (`docs: reference/cli-commands.md:536-575`).
- Los dos le hablan a la sesión Bot Chat de Basecamp, no a un tablero.

**Seguridad** (`docs: user-guide/security.md`).

- Los contextos sin nadie mirando (cron, `-q`, webhook y API) rechazan los comandos peligrosos por defecto.
- Los backends en contenedor se saltan esa revisión, porque el contenedor es el límite.
- En Basecamp, `coder` y `qa` corren cada tarjeta en un contenedor desechable (1 CPU, 2.560 MB).
  `orchestrator` y `publisher` corren en `local`, dentro del contenedor `hermes`, que tiene montado
  `docker.sock`: eso equivale a root en el host.
- Los logs repiten una advertencia: «API server is network-accessible (0.0.0.0) AND the terminal backend is
  'local'». Ahí falta verificar qué ve de verdad la red.

## 5. Cómo está hoy y qué roza

Basecamp: Hermes `v0.21.6` sobre la imagen base `rc.4-v0.21.6`, y `factory v0.1.0` fijado con su `sha256`.
Hay cinco perfiles (`default`, `orchestrator`, `coder`, `qa` y `publisher`), todos con `deepseek-v4.1-flash`,
y no hay ningún job de cron. Los tableros: `factory-sandbox` (43 tarjetas hechas), `torresposso-site` (34) y
`laiacaliente-app` (9 archivadas). Casi todas las corridas terminaron bien; solo dos se bloquearon.

| Qué roza | Efecto |
|---|---|
| `~/AGENTS.md` dice planner/coder/tester/reviewer y que laiacaliente no está clonado | Los perfiles son orchestrator/coder/qa/publisher y el repo **sí** está clonado |
| El `SOUL.md` de `publisher` le dice que escriba «Merge danger» | Eso ya lo arma `factory publish` en código |
| La skill `implement` dice «usa /code-review» | La regla de la tarjeta lo prohíbe; hoy gana la regla solo porque viene en la tarjeta |
| Contrato del Makefile: solo `laiacaliente-app` tiene `verify`, `preview` y `preview-stop` | En `factory-sandbox` y `torresposso.site` falla el `make preview-stop` que pide la regla del coder |
| `security.protected_instruction_files` | Una tarjeta que tenía que editar `AGENTS.md` quedó bloqueada (`t_adcd2ca1`) |
| `laiacaliente-app` todavía tiene `cmd/factory` y `make factory-plan` | Es una copia vieja que se va a seguir separando |
| La trampa del ticket suelto (`docs/hermes.md`) | Sigue igual |
| La rama `evidence/<N>` nunca se borra | Se van acumulando ramas |

## 6. Qué haría, en orden

1. **Actualizar Hermes a `v0.22.5`** en la imagen de Basecamp. La versión va fijada en el `Dockerfile`, con
   la imagen oficial. Gana robustez sin romper la CLI que usa `factory`. Después se corre `factory plan
   --dry-run` contra un tablero de prueba.
2. **Probar el hook `kanban_task_completed`** en `factory-sandbox`. Si funciona, la tarjeta Publish puede
   cambiarse por un hook que corra `factory publish`. Eso quita el paso que hoy depende de un modelo. Antes
   hay que corregir `docs/hermes.md`.
3. ~~**Pasar el webhook a `factory plan`**~~. Hecho de otra forma el 2026-10-24: se quitó el webhook. La
   orden de arrancar la da él, que es quien publica el spec, así que `factory plan` es la única fuente de
   verdad y Basecamp ya no expone nada a internet.
4. **Poner a punto las tarjetas**: `--max-retries 1` en Publish, leer `runs[]` desde `show --json` y
   agregar `diagnostics --json` a la salida.
5. **Limpiar lo que se separó**: el `SOUL.md` de `publisher`, la skill `implement`, `~/AGENTS.md` (perfiles
   y clones) y la copia de `cmd/factory` en `laiacaliente-app`.
6. **Contrato del Makefile**: que `factory-sandbox` y `torresposso.site` tengan `preview` y `preview-stop`,
   o que la regla del coder se salte la vista previa cuando no existan.
7. **Costo**: un modelo mejor solo en `qa` (la revisión) si las revisiones se quedan cortas, `agent.max_turns`
   más bajo en los workers y `--model` en una tarjeta difícil. Los precios se consultan en la página del
   proveedor el día que se decida.
8. **Decidir**: si se acepta un script en `$HERMES_HOME/scripts/` para el cron silencioso, o se usa un cron
   con modelo que conteste `[SILENT]`.

## 7. Lo que sigue abierto

- Si el gateway multiplexado lee `kanban.max_in_progress` del perfil `default` o de otro.
- Hasta dónde llega `security.protected_instruction_files`, y si se puede abrir por tarjeta para los tickets
  que tienen que editar `AGENTS.md`.
- Si un worker en un workspace `dir:` carga solo el `AGENTS.md` del repo. Se sabe que hace `chdir` al
  workspace (`gateway/session_kanban.py:195`), pero la regla de la tarjeta igual le dice que lo lea.
- Quién borra los contenedores por tarjeta del backend `docker` después de un timeout o de un reinicio.
- Si hay un tope de gasto nativo por perfil o por tarjeta.
