# IronLark

[English](README.md) | [French](README.fr.md) | [Spanish](README.es.md) | [日本語](README.ja.md) | [中文文档](README.zh-CN.md)

IronLark est un operateur IA natif SSH, concu pour vous aider directement dans le terminal quand vous etes connecte a une machine distante et que vous devez inspecter, corriger, surveiller et rendre compte sans quitter la session.

Cette page est un apercu en francais. La documentation anglaise dans `README.md` reste la reference la plus complete et la plus a jour.

## Pourquoi utiliser IronLark

Utilisez IronLark lorsque vous voulez un agent qui travaille comme un operateur directement dans votre session SSH :

- inspecter un serveur, des logs, des configs, des processus, des ports et des repos
- garder un contexte persistant entre des commandes ponctuelles et `lk agent`
- lancer une recuperation en arriere-plan et revenir plus tard voir si le service est redevenu stable
- surveiller un service en continu, capturer des preuves et gerer automatiquement les incidents de redemarrage evidents
- garder une memoire operationnelle locale de l'historique, des incidents, des watchers et des recuperations
- utiliser `lk ps` comme plan de controle d'urgence pour arreter un run bloque

## Comment IronLark fonctionne

IronLark est concu pour reduire la friction dans un vrai workflow terminal :

- il commence par regarder le contexte local utile de la machine et du repo
- il execute les inspections simples et peu risquées sans vous interrompre inutilement
- il s'arrete sur des limites d'approbation claires pour les commandes risquées et les modifications de fichiers
- il memorise ce qui a deja ete decouvert afin que les questions suivantes ne repartent pas de zero
- il garde l'historique des travaux en arriere-plan et des incidents localement sur la machine

L'objectif n'est pas d'etre un chatbot generaliste dans un terminal. L'objectif est de vous aider a passer rapidement de "quelque chose ne va pas sur cette machine" a "je comprends ce qui se passe et quoi faire ensuite".

## Quand IronLark est le bon choix

IronLark est particulierement utile pour :

- le debug d'un serveur en direct via SSH
- la recuperation d'un service et son suivi dans le temps
- l'analyse d'incidents directement sur la machine concerne
- les modifications prudentes de fichiers de configuration dans le terminal

Si votre besoin principal est un workflow d'IDE tres large ou un agent de developpement generaliste, `README.md` explique aussi les compromis.

## Demarrage rapide

### Machine locale

```bash
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
mkdir -p ~/.config/lark
cat > ~/.config/lark/.env <<'EOF'
OPENAI_API_KEY=your_key_here
EOF
lk init
lk version
lk model
lk config test
lk "hello"
```

### Serveur distant via SSH

```bash
ssh root@your-server-ip
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
lk init
lk "what can you help me do on this server?"
lk agent
```

## Workflows operateur

### Recuperer un service

```bash
lk recover "restore openclaw and keep going until it is stable"
```

### Surveiller un service

```bash
lk watch openclaw
```

### Inspecter les travaux en arriere-plan

```bash
lk ps
lk watch list
lk recover list
```

## Commandes utiles

- `lk "task"` : execution ponctuelle en mode execute-first
- `lk --plan "task"` : afficher un plan visible avant execution
- `lk agent` : session interactive SSH-first
- `lk edit <path> [instruction]` : modifier un fichier avec apercu du diff
- `lk run "<command>"` : executer une commande shell avec garde-fous
- `lk context` : voir le contexte persistant actif
- `lk policy list` : voir les regles machine
- `lk ps` : voir les processus IronLark actifs

## Open Source

- Licence : GNU Affero General Public License v3.0 (AGPL-3.0)
- Commandes : `lark` et `lk`
- Nom du projet : IronLark
