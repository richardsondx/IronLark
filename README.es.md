# IronLark

[English](README.md) | [French](README.fr.md) | [Spanish](README.es.md) | [日本語](README.ja.md) | [中文文档](README.zh-CN.md)

IronLark es un operador de IA pensado para SSH, creado para ayudarte directamente dentro del terminal cuando entras en una maquina remota y necesitas inspeccionar, corregir, vigilar y reportar sin salir de la sesion.

Esta pagina es un resumen en espanol. La documentacion en `README.md` sigue siendo la referencia mas completa y actualizada.

## Por que usar IronLark

Usa IronLark cuando quieras un agente que trabaje como un operador real dentro de tu sesion SSH:

- inspeccionar servidores, logs, configuraciones, procesos, puertos y repositorios
- mantener contexto persistente entre comandos puntuales y `lk agent`
- lanzar una recuperacion en segundo plano y volver mas tarde para ver si el servicio ya esta estable
- vigilar un servicio continuamente, capturar evidencia y manejar incidentes obvios de reinicio
- mantener memoria operativa local de watchers, recuperaciones, incidentes y auditoria
- usar `lk ps` como plano de control de emergencia para detener un proceso atascado

## Como funciona IronLark

IronLark esta disenado para reducir friccion en flujos reales de terminal:

- primero mira el contexto local util de la maquina y del repo
- ejecuta inspecciones simples y seguras sin pedir aprobacion para cada lectura pequena
- se detiene en limites claros de aprobacion para comandos riesgosos y cambios de archivos
- recuerda lo que ya descubriste para que los siguientes prompts no se sientan sin contexto
- guarda historial local de trabajo en segundo plano, incidentes y recuperaciones

La idea no es ser un chatbot generico en un terminal. La idea es ayudarte a pasar de "algo esta mal en esta maquina" a "entiendo que paso y cual es el siguiente paso" con menos friccion.

## Cuando encaja mejor IronLark

IronLark es especialmente util para:

- depurar un servidor en vivo por SSH
- recuperar un servicio y revisarlo despues
- seguir incidentes a traves de varias sesiones de terminal
- editar con cuidado archivos de configuracion directamente en la maquina

Si tu necesidad principal es un flujo amplio de ingenieria de software conectado al IDE, el `README.md` principal explica mejor ese contraste.

## Inicio rapido

### Maquina local

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

### Servidor remoto por SSH

```bash
ssh root@your-server-ip
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
lk init
lk "what can you help me do on this server?"
lk agent
```

## Flujos de operador

### Recuperar un servicio

```bash
lk recover "restore openclaw and keep going until it is stable"
```

### Vigilar un servicio

```bash
lk watch openclaw
```

### Inspeccionar trabajo en segundo plano

```bash
lk ps
lk watch list
lk recover list
```

## Comandos utiles

- `lk "task"`: flujo puntual en modo execute-first
- `lk --plan "task"`: muestra un plan visible antes de ejecutar
- `lk agent`: sesion interactiva orientada a SSH
- `lk edit <path> [instruction]`: cambia un archivo con revision de diff
- `lk run "<command>"`: ejecuta un comando shell con guardrails
- `lk context`: muestra el contexto persistente activo
- `lk policy list`: muestra reglas de la maquina
- `lk ps`: muestra procesos activos de IronLark

## Open Source

- Licencia: GNU Affero General Public License v3.0 (AGPL-3.0)
- Comandos: `lark` y `lk`
- Nombre del proyecto: IronLark
