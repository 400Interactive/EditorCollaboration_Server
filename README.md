# Serveur Editor Collaboration en Rust

Le serveur implémente le protocole 9 du plugin Unreal. Il fournit la présence, le chat, les commentaires, les pings, les marques de travail, la publication et la suppression de packages, la synchronisation des graphes et niveaux, le journal de reprise et le transfert de fichiers. Les données utilisent le schéma SQLite et l’arborescence `blobs` du serveur Go.

## Démarrer sous Windows

Depuis `Server_Rust`, avec Rust stable et les outils C++ de Visual Studio disponibles.

```powershell
./build.ps1
./dist/collab-server-rust-windows-amd64.exe -addr 127.0.0.1:7878 -data ./data
```

Dans le plugin, saisir `ws://127.0.0.1:7878/ws` et le nom du salon. Pour un essai avec un serveur Go déjà à l’écoute sur 7878, choisir un autre port et un dossier de données distinct.

Les options `-detach`, `-stop` et `-healthcheck` acceptent le même `-data` que le serveur. Sous Windows, `-no-tray` masque l’icône de notification. `-log` écrit aussi dans un fichier.

## Héberger le serveur

- Pour un salon protégé, définir `COLLAB_ROOM` et `COLLAB_PASSWORD`. Pour plusieurs salons, définir `COLLAB_ROOM_PASSWORDS_FILE` sur un fichier JSON qui associe chaque nom de salon à un mot de passe distinct.
- `COLLAB_HOST`, `COLLAB_PORT`, `COLLAB_DATA_DIR` et `COLLAB_MAX_ROOM_CLIENTS` règlent l’écoute et le stockage. Le serveur exige un mot de passe pour écouter hors boucle locale.
- Pour WSS direct, fournir les fichiers PEM avec `-tls-cert` et `-tls-key`. L’exemple Caddy dans `deploy` montre comment terminer TLS sur un proxy.
- Sous Linux ou macOS, utiliser `sh build.sh`. Les scripts de compilation acceptent aussi une cible Rust installée et son compilateur C. Ils écrivent les binaires et les sommes SHA-256 dans `dist`.
- Pour Docker, renseigner `COLLAB_ROOM` et `COLLAB_PASSWORD`, puis lancer `docker compose up -d --build` depuis ce dossier. Le port hôte peut être réglé avec `COLLAB_PUBLISHED_PORT`.
- Sous Debian avec systemd, exécuter `sudo sh deploy/install-debian.sh <binaire-linux>`. Le service et son dossier de données ont des noms distincts de ceux du serveur Go. Modifier `/etc/editor-collab-rust.env` pour autoriser les clients distants, puis redémarrer le service.

Les commandes `-check-update` et `-update` recherchent les exécutables `collab-server-rust-<os>-<arch>` et leur entrée dans `SHA256SUMS` sur le dépôt de releases configuré par `COLLAB_GITHUB_REPO`. Le suffixe Windows est `.exe`. `COLLAB_AUTO_UPDATE=1` permet d’installer les mises à jour dans les installations où le processus peut écrire son exécutable. Pour systemd, l’installateur fournit la commande `--update` à exécuter avec les droits d’installation. `COLLAB_UPDATE_CHECK=0` désactive la vérification périodique.

## Réutiliser les données Go

Arrêter Go, sauvegarder son dossier de données complet, puis passer la copie avec `-data`. Rust ouvre la base SQLite, ajoute les colonnes manquantes et importe les anciens fichiers `room-*.json`. Conserver ensemble `collab.db` et le dossier `blobs`.

## Vérifier le protocole

```powershell
python -m pip install websockets==17.4
python tests/protocol.py dist/collab-server-rust-windows-amd64.exe
python tests/protocol.py dist/collab-server-rust-windows-amd64.exe <serveur-Go>
```

Le second argument fait créer et remplir le dossier de données temporaire par Go, puis relit les données et le journal avec Rust. Les deux scénarios passent par de vraies connexions HTTP et WebSocket.

## GitHub Actions

Le workflow compile et teste le serveur Rust à chaque pull request et push sur `main`. Les binaires sont disponibles dans les artefacts du run pour Windows x64, Linux x64 et ARM64, et macOS Intel et Apple Silicon.

Pour publier une release, pousser un tag qui correspond à la version de `Cargo.toml` (par exemple `1.4.0` ou `v1.4.0`). La publication ajoute les cinq binaires et le fichier `SHA256SUMS` après la réussite de tous les tests.
