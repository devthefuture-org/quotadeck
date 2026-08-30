# Mission : construire **QuotaDeck**, un dashboard local multi-forfaits IA pour Linux

Tu es l’agent lead engineer chargé de concevoir et d’implémenter un MVP réellement exécutable. Ne t’arrête pas à un audit ou à un plan : inspecte l’environnement, écris le code, lance les tests, démarre l’application et laisse le dépôt dans un état utilisable.

## 1. Contexte utilisateur

L’utilisateur travaille sous Ubuntu/Debian et veut suivre, dans une seule interface locale :

- plusieurs comptes Claude/Anthropic gérés par `cswap` ;
- les quotas Codex inclus dans plusieurs forfaits ChatGPT/OpenAI ;
- un ou plusieurs forfaits Z.ai / GLM Coding Plan ;
- toutes les fenêtres retournées par chaque provider : 5 h, hebdomadaire, mensuelle, par modèle, MCP, crédits, etc. ;
- le forfait détecté, le pourcentage consommé/restant, le reset, l’âge de la mesure et les erreurs ;
- une mise à jour quasi temps réel, un historique local et une installation propre sur Ubuntu.

CrossUsage ne répond pas correctement au besoin : dans le binaire installé, l’interface n’expose pas la gestion des credentials/comptes par provider et Claude ne reflète que le compte actif. Ne cherche pas à contourner son interface. Construis un modèle multi-compte et multi-fenêtre de premier ordre.

## 2. Décisions produit non négociables

1. **Web local d’abord, desktop shell ensuite.**
   - Un daemon local sert une UI web sur `127.0.0.1`.
   - Le frontend est embarqué dans le binaire final.
   - Un tray/Tauri pourra être ajouté plus tard uniquement pour ouvrir/masquer l’UI.

2. **Le cœur du domaine est `provider -> accounts -> windows[]`.**
   - Aucun provider ne doit être limité à deux champs codés en dur `session` et `weekly`.
   - Toute fenêtre valide retournée par une source doit pouvoir être stockée, exposée par l’API et rendue automatiquement.

3. **`cswap` reste la source canonique pour Claude multi-compte.**
   - Ne lis, ne copies et ne stocke jamais les refresh tokens Claude.
   - Consomme `cswap list --json` et normalise son résultat.
   - Le dashboard ne doit pas devenir un second coffre OAuth.

4. **Local-first et zéro télémétrie.**
   - Bind par défaut : `127.0.0.1` uniquement.
   - Aucun analytics, crash reporter ou appel cloud hors endpoints nécessaires aux providers.
   - Aucun secret dans les logs, l’API, SQLite, les fixtures, les captures ou les messages d’erreur.

5. **Découverte explicable.**
   - Pour chaque compte/provider, l’UI doit indiquer la source détectée : `cswap`, variable d’environnement, fichier Claude, `CODEX_HOME`, configuration explicite, etc.
   - Ajouter une commande `quotadeck doctor` qui explique ce qui est détecté sans afficher de secret.

6. **Licence.**
   - Crée par défaut un projet greenfield sous licence MIT.
   - `onWatch` est GPL-3.0 : l’utiliser comme référence d’architecture est permis, mais ne copie pas son code dans un projet MIT.
   - Le code MIT de `claude-swap`, CrossUsage et CodexBar peut être étudié/réutilisé uniquement en conservant les notices requises et en documentant précisément tout code repris.
   - Si tu estimes qu’un fork GPL d’onWatch est nettement préférable, écris d’abord une ADR expliquant ce choix et conserve obligatoirement GPL-3.0. Ne relisence jamais silencieusement du code.

## 3. Stratégie recommandée

Construis un nouveau cœur générique en Go et utilise des adaptateurs de sources. N’implémente pas un gros framework de plugins avant que les trois providers prioritaires fonctionnent.

### Stack cible

- Backend : Go, HTTP local, goroutines par source/provider.
- Base locale : SQLite en WAL, idéalement via un driver pur Go pour faciliter le binaire et le `.deb`.
- Frontend : TypeScript + Vite + React ou Preact, sans dépendance à un backend Node en production.
- Temps réel UI : Server-Sent Events ; le compte à rebours des resets évolue côté navigateur chaque seconde.
- Assets frontend embarqués avec `embed.FS`.
- Configuration : YAML ou TOML sous `${XDG_CONFIG_HOME:-~/.config}/quotadeck/config.yaml`.
- Données : `${XDG_DATA_HOME:-~/.local/share}/quotadeck/quotadeck.db`.
- Logs : `${XDG_STATE_HOME:-~/.local/state}/quotadeck/`.
- Packaging : GoReleaser + nfpm pour produire un `.deb`, plus un service systemd **utilisateur**.

### Arborescence suggérée

```text
quotadeck/
  cmd/quotadeck/
  internal/domain/
  internal/config/
  internal/runner/
  internal/provider/
    claudecswap/
    zai/
    codex/
  internal/poller/
  internal/store/
  internal/httpapi/
  internal/doctor/
  web/
  packaging/systemd/
  docs/adr/
  testdata/
  Makefile
  README.md
  LICENSE
```

## 4. Contrat de domaine générique

Crée des types proches de ceux-ci, en les ajustant si nécessaire :

```go
type Account struct {
    ID           string            `json:"id"`
    ProviderID   string            `json:"providerId"`
    Label        string            `json:"label"`
    Plan         string            `json:"plan,omitempty"`
    Active       bool              `json:"active"`
    Disabled     bool              `json:"disabled,omitempty"`
    Source       string            `json:"source"`
    SourceMeta   map[string]string `json:"sourceMeta,omitempty"`
}

type QuotaWindow struct {
    ID                    string     `json:"id"`
    Label                 string     `json:"label"`
    Kind                  string     `json:"kind"`
    Scope                 string     `json:"scope,omitempty"`
    UsedPercent           *float64   `json:"usedPercent,omitempty"`
    RemainingPercent      *float64   `json:"remainingPercent,omitempty"`
    Used                  *float64   `json:"used,omitempty"`
    Limit                 *float64   `json:"limit,omitempty"`
    Remaining             *float64   `json:"remaining,omitempty"`
    Unit                  string     `json:"unit,omitempty"`
    StartsAt              *time.Time `json:"startsAt,omitempty"`
    ResetsAt              *time.Time `json:"resetsAt,omitempty"`
    ExpectedPercent       *float64   `json:"expectedPercent,omitempty"`
    ProjectedExhaustionAt *time.Time `json:"projectedExhaustionAt,omitempty"`
    WillLastToReset       *bool      `json:"willLastToReset,omitempty"`
}

type Snapshot struct {
    AccountID    string        `json:"accountId"`
    FetchedAt    time.Time     `json:"fetchedAt"`
    SourceAgeSec *int64        `json:"sourceAgeSeconds,omitempty"`
    Status       string        `json:"status"`
    Stale        bool          `json:"stale"`
    ErrorCode    string        `json:"errorCode,omitempty"`
    ErrorMessage string        `json:"errorMessage,omitempty"`
    Windows      []QuotaWindow `json:"windows"`
}

type Provider interface {
    ID() string
    Discover(ctx context.Context) ([]AccountCandidate, error)
    Fetch(ctx context.Context, account AccountCandidate) (Account, Snapshot, error)
}
```

Règles :

- identité stable et opaque ; ne jamais utiliser une clé/token brut comme identifiant ;
- tolérer les champs inconnus des sources ;
- conserver le dernier snapshot valide lorsqu’une source devient temporairement indisponible ;
- distinguer `fresh`, `stale`, `auth_error`, `unavailable`, `unsupported` ;
- tous les pourcentages sont normalisés en **consommé**, l’UI pouvant afficher consommé ou restant ;
- ne jamais déduire silencieusement qu’une fenêtre inconnue est « weekly » : conserver son identifiant et son libellé source.

## 5. Adaptateur Claude via `cswap`

### Source

Exécuter directement, sans shell :

```bash
cswap list --json
```

### Comportement requis

- détecter `cswap` via `exec.LookPath` ;
- timeout et annulation via `exec.CommandContext` ;
- accepter `schemaVersion: 1` et ignorer les nouveaux champs additifs ;
- créer une carte par entrée de `accounts[]` ;
- afficher l’alias en priorité, puis l’email, puis `Claude account N` ;
- conserver `active`, `disabled`, `usageStatus` et les informations d’âge ;
- mapper :
  - `usage.fiveHour` ;
  - `usage.sevenDay` ;
  - toutes les entrées dynamiques de `usage.scoped` ;
  - `lastGoodUsage` lorsque `usage` est absent, en marquant le snapshot stale/non-actionnable ;
  - `expectedPct`, `aheadOfPace`, `projectedExhaustionAt`, `willLastToReset` lorsqu’ils existent ;
- ne jamais appeler `cswap export` ;
- ne jamais lire le coffre de credentials de `cswap` ;
- ne jamais rafraîchir les OAuth Claude toi-même.

Pour l’identité MVP, utiliser une forme stable et non secrète telle que `claude:cswap:<slot>`, conformément au slot exposé par `cswap`. Conserver l’email uniquement comme label affichable.

Une action de switch est hors du chemin critique. Après le dashboard read-only, elle pourra être ajoutée par une route explicite qui exécute :

```bash
cswap switch <slot> --json
```

avec validation stricte du slot, protection CSRF locale et aucune interpolation shell.

## 6. Adaptateur Z.ai

### Découverte des clés, par priorité

1. comptes explicitement configurés avec une référence vers une variable d’environnement ;
2. `ZAI_API_KEY` ;
3. `GLM_API_KEY` ;
4. `~/.claude/settings.json`, uniquement si :
   - `.env.ANTHROPIC_BASE_URL` correspond à un endpoint Z.ai/BigModel reconnu ;
   - `.env.ANTHROPIC_AUTH_TOKEN` est présent ;
5. chemins supplémentaires explicitement configurés.

Le cas principal à supporter est :

```json
{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "<secret>",
    "ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic"
  }
}
```

Ne considère jamais un `ANTHROPIC_AUTH_TOKEN` comme une clé Z.ai si le base URL ne correspond pas à Z.ai. Ne journalise jamais la valeur. Pour dédupliquer, utilise seulement une empreinte SHA-256 tronquée calculée en mémoire.

### Récupération et parsing

- implémenter le client avec timeout, backoff, jitter et respect de `Retry-After` ;
- étudier les implémentations MIT de CrossUsage et CodexBar pour connaître les variantes globales/China, personnelles/Team et les formes de réponse ;
- réimplémenter proprement le parser et ajouter des fixtures redacted ;
- ne jamais supposer qu’il n’y a que deux limites ;
- parcourir dynamiquement toutes les limites retournées ;
- supporter au minimum les champs rencontrés comme `type`, `unit`, `number`, `usage`, `currentValue`, `remaining`, `percentage`, `nextResetTime`, `windowMinutes` et leurs variantes ;
- exposer le plan, l’organisation/workspace éventuels et toutes les fenêtres ;
- permettre plusieurs comptes Z.ai via plusieurs références de variables d’environnement, par exemple :

```yaml
providers:
  zai:
    enabled: true
    accounts:
      - label: personal
        keyEnv: ZAI_API_KEY_PERSONAL
        region: global
      - label: team
        keyEnv: ZAI_API_KEY_TEAM
        region: global
        organizationIdEnv: ZAI_ORG_ID_TEAM
        workspaceIdEnv: ZAI_WORKSPACE_ID_TEAM
```

## 7. Adaptateur OpenAI Codex / forfait ChatGPT

Objectif : afficher les fenêtres Codex du forfait ChatGPT, pas la facturation OpenAI API classique.

### Sources MVP

1. `codex app-server` / JSON-RPC, en étudiant les méthodes de lecture de compte et de rate limits utilisées par CodexBar ;
2. relecture de l’état d’authentification local sous chaque `CODEX_HOME`, sans stocker ni recopier les tokens ;
3. fallback optionnel vers le CLI `codexbar` si présent, derrière un adaptateur séparé et clairement signalé comme fallback.

### Multi-compte

Supporter plusieurs homes explicitement :

```yaml
providers:
  codex:
    enabled: true
    accounts:
      - label: personal
        home: ~/.codex
      - label: work
        home: ~/.codex-work
```

Pour chaque compte, lancer les commandes avec un environnement `CODEX_HOME` isolé. Ne modifie jamais `auth.json`. Si une session doit être renouvelée, afficher une instruction de réauthentification avec le CLI Codex au lieu de devenir un gestionnaire OAuth.

Ne dépends pas des cookies du dashboard ChatGPT dans le MVP. Le support de données web supplémentaires pourra être ajouté plus tard comme source opt-in.

## 8. Polling, stockage et temps réel

- un worker indépendant par compte/source ;
- intervalle configurable, défaut raisonnable entre 30 et 120 secondes ;
- empêcher deux fetches simultanés du même compte ;
- backoff exponentiel borné sur erreur ;
- jitter pour éviter les rafales ;
- transaction SQLite par snapshot ;
- WAL activé ;
- rétention configurable, par défaut 30 jours ;
- persister les comptes, snapshots et fenêtres de façon générique ;
- fournir une migration versionnée dès le départ.

API minimale :

```text
GET  /api/v1/state
GET  /api/v1/providers
GET  /api/v1/accounts
GET  /api/v1/accounts/{id}/history?from=&to=
GET  /api/v1/health
GET  /api/v1/doctor
GET  /api/v1/events              # SSE
POST /api/v1/refresh
```

`POST /refresh` doit être protégé contre les appels concurrents et rester local-only.

## 9. Interface MVP

Vue principale :

- groupes Claude, Codex, Z.ai ;
- une carte par compte ;
- badge du forfait ;
- badge `active` pour le compte Claude courant ;
- état `fresh/stale/error` et âge de la mesure ;
- toutes les fenêtres en lignes générées dynamiquement ;
- barre consommée/restante ;
- reset relatif et date absolue au survol ;
- fenêtre la plus contrainte mise en évidence ;
- tri par provider, puis compte, puis reset ;
- filtre `tous / provider / compte` ;
- mode clair/sombre automatique ;
- responsive, utilisable dans une fenêtre étroite.

Page diagnostics :

- versions de QuotaDeck, `cswap`, `codex` et éventuellement `codexbar` ;
- chemin des fichiers détectés ;
- booléen « secret présent » sans valeur ;
- source choisie et sources rejetées avec raison ;
- dernier statut HTTP/provider redacted ;
- bouton copier un diagnostic JSON totalement redacted.

Ne reproduis pas la limitation CrossUsage où seuls `session` et `weekly` peuvent être cochés. Toutes les fenêtres sont visibles par défaut ; un masquage éventuel doit cibler un `window.id` dynamique.

## 10. Commandes développeur attendues

Le dépôt doit fournir :

```bash
make dev
make test
make test-race
make lint
make build
make package-deb
./dist/quotadeck doctor
./dist/quotadeck serve --bind 127.0.0.1 --port 9211
```

Ajouter également :

```bash
quotadeck doctor --json
quotadeck refresh
quotadeck status --json
quotadeck service install --user
quotadeck service status
quotadeck service uninstall --user
```

## 11. Inspection initiale sûre

Avant de coder, recueille seulement des métadonnées non secrètes. Ne fais jamais `cat` d’un fichier d’authentification.

Exemples autorisés :

```bash
set -euo pipefail
command -v cswap || true
command -v codex || true
command -v codexbar || true
cswap --version 2>/dev/null || true
codex --version 2>/dev/null || true
codexbar --version 2>/dev/null || true

# Sortie cswap documentée comme machine-readable ; masque les emails dans les logs de travail.
cswap list --json 2>/dev/null \
  | jq 'if .accounts then .accounts |= map(.email = (if .email then "<redacted>" else null end)) else . end' \
  || true

# Afficher seulement la présence et le base URL Z.ai, jamais le token.
jq '{
  baseUrl: (.env.ANTHROPIC_BASE_URL // null),
  hasAnthropicAuthToken: ((.env.ANTHROPIC_AUTH_TOKEN // "") | length > 0)
}' "$HOME/.claude/settings.json" 2>/dev/null || true

# Pour les fichiers sensibles, inspecter uniquement les chemins de clés JSON.
jq -r 'paths(scalars) | map(tostring) | join(".")' \
  "$HOME/.codex/auth.json" 2>/dev/null || true
```

Si une commande de diagnostic risque malgré tout d’imprimer un token, ne l’exécute pas ; écris d’abord un petit outil de redaction robuste.

## 12. Repositories de référence à inspecter

Clone-les dans un dossier temporaire, en shallow clone, puis note le commit SHA et la licence dans `docs/adr/0001-reference-projects.md` :

```bash
mkdir -p /tmp/quotadeck-references
cd /tmp/quotadeck-references

git clone --depth 1 https://github.com/realiti4/claude-swap.git
git clone --depth 1 https://github.com/steipete/CodexBar.git
git clone --depth 1 https://github.com/barramee27/crossusage.git
git clone --depth 1 https://github.com/onllm-dev/onwatch.git
```

À étudier en priorité :

- `claude-swap` : contrat `cswap list --json`, staleness, scoped windows, slots ;
- CodexBar : décision d’architecture Claude + `claude-swap`, provider-neutral snapshots, Codex app-server, parser Z.ai ;
- CrossUsage : plugins Z.ai/Codex et packaging Tauri Linux, uniquement comme référence ;
- onWatch : daemon Go, SQLite WAL, SSE/web dashboard, systemd, sans copier de code GPL dans un projet MIT.

## 13. Plan d’exécution obligatoire

1. Créer `docs/adr/0001-reference-projects.md` avec licences, SHA et décisions de réutilisation.
2. Créer `docs/adr/0002-domain-model.md` expliquant le modèle dynamique provider/account/window.
3. Scaffolder le backend, la DB, l’API et un frontend minimal.
4. Implémenter d’abord la tranche verticale **Claude via cswap** : discovery -> fetch -> DB -> API -> SSE -> carte UI.
5. Ajouter les tests avec fixtures pour `usage`, `lastGoodUsage`, `scoped`, compte désactivé et erreur de schéma.
6. Implémenter Z.ai avec découverte depuis `~/.claude/settings.json` et plusieurs comptes via env refs.
7. Implémenter Codex avec un `CODEX_HOME`, puis plusieurs homes.
8. Ajouter `quotadeck doctor` et vérifier qu’aucun secret ne fuit.
9. Ajouter historique, rétention, backoff et rafraîchissement manuel.
10. Produire le `.deb` et le service systemd utilisateur.
11. Installer localement le `.deb`, démarrer le service et effectuer un smoke test HTTP/UI.
12. Mettre à jour le README avec installation, configuration, sécurité et dépannage.

Fais des commits atomiques après chaque tranche fonctionnelle. Ne pousse rien vers un remote sans demande explicite.

## 14. Critères d’acceptation du MVP

Le travail n’est terminé que lorsque :

- `quotadeck doctor` détecte `cswap` et annonce le nombre de comptes sans afficher de token ;
- tous les comptes Claude apparaissent simultanément ;
- le compte actif et les comptes désactivés sont distingués ;
- les fenêtres 5 h, 7 jours et chaque fenêtre `scoped` apparaissent dynamiquement ;
- les données stale de `lastGoodUsage` restent visibles avec un avertissement clair ;
- Z.ai est détecté depuis `~/.claude/settings.json` lorsque le base URL correspond, sans variable `ZAI_API_KEY` supplémentaire ;
- toutes les limites Z.ai retournées sont affichées, pas seulement deux ;
- au moins un compte Codex fonctionne et plusieurs `CODEX_HOME` peuvent être configurés ;
- l’UI reçoit les changements via SSE sans reload manuel ;
- SQLite conserve l’historique après redémarrage ;
- le service écoute uniquement sur `127.0.0.1` par défaut ;
- les tests unitaires et race tests passent ;
- un `.deb` Ubuntu/Debian est produit et installable ;
- une recherche automatisée de patterns de secrets sur les logs, fixtures et artefacts ne trouve rien.

## 15. Rapport final attendu de l’agent

À la fin, fournis :

1. les décisions d’architecture prises ;
2. la liste des fichiers majeurs créés/modifiés ;
3. les commandes exactes pour installer et lancer ;
4. les résultats des tests ;
5. les providers/comptes réellement détectés, avec identifiants personnels redacted ;
6. les limites restantes et les prochaines étapes prioritaires ;
7. le chemin exact du `.deb` généré.

Commence maintenant par l’inspection sûre de l’environnement et des quatre repositories, puis implémente la tranche Claude/cswap complète avant de passer à Z.ai et Codex.
