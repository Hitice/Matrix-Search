# Arquitetura da Matrix-Search Pool (Golang)

Este documento descreve o Blueprint arquitetural para escalar o **SECP256 Key Worker Dashboard** de uma ferramenta local para uma **Pool de Mineração Distribuída**, inspirada pelas operações de resolução dos maiores Puzzles do Bitcoin.

---

## 1. Visão Geral do Sistema (SaaS Blueprint)

A arquitetura transformará o monolito atual (`secp256-master.exe`) em dois componentes destintos:
1. **Pool Master Server (O Cérebro Central)**: Servidor escalável rodando em cloud (ex: AWS, Google Cloud) que orquestra a divisão do puzzle, cuida de pagamentos e previne fraudes de agentes falsos.
2. **Miner Agent (O Cliente)**: O software leve (sem dashboard) que o usuário da pool baixa, roda na própria máquina e empresta a GPU para a rede.

---

## 2. Network Layer (Comunicação Master ⟷ Miner)

- **Protocolo Base**: `gRPC` com Streams Dúplex Bidirecionais ou `WebSockets` com compressão `zlib`.
- **Registro do Minerador**: O Minerador se conecta passando seu endereço de pagamento e ID de Hardware (GPU). Exemplo: `ws://pool.hitice.com:8080/join?wallet=bc1q...`
- **Fatiamento de Trabalho (Chunking)**:
  O Master Server quebra o The Big Puzzle (ex: 135) em "micro-ranges".
  - *Tamanho do Range por Job*: 1 a 10 Bilhões de chaves (ajustado dinamicamente baseado no Throughput do Minerador).
  - O Master envia: `{"job_id": 9012, "start": "40000000000", "end": "40000010000"}`
- **Heartbeat & Telemetria**:
  A cada 10 segundos, o Minerador envia um payload leve informando onde está no range e seu P/s.

---

## 3. Segurança Anti-Fraude e Prova de Trabalho

Evitar que um Minerador minta que leu bilhões de chaves só para ganhar "Shares" falsos:
- **Pontos Distintos (Distinguished Points Verification)**: Em intervalos aleatórios, o Worker deve encontrar pontos específicos na curva (por força bruta regular, pontos cujo hash termine em `0x000`). Se o Worker passar pelo Range e não submeter o ponto específico nos metadados do pacote de progresso, o Master invalida a requisição por fraude (**Drop and Ban**).
- **Redundância**: Ranges de altíssima incerteza são despachados silenciosamente para 2 Mineradores distintos simulaneamente para averiguação cruzada.

---

## 4. Dinâmica de Payout e Transferência Fria (Cold Sweep)

Imaginando uma recompensa de 6.6 BTC para o Puzzle:
1. **O Gatilho de Hit**: Um Minerador encontra a chave e transmite: `{"event": "HIT", "priv": "AFFE938..."}` através de canal TLS criptografado (AES-256-GCM).
2. **The Sweep**: O Pool Master verifica instantaneamente a chave contra a Public Key do alvo.
   Ao ser declarada válida, um nó local do Bitcoin Server (Bitcoin Core) do Master instantaneamente invoca um script RPC `sendrawtransaction`.
3. **Distribuição**:
   O fundo total (6.6 BTC) "estoura" em micro-distribuições via a carteira de consolidação da Pool.
   - **Finder's Reward**: 10% (0.66 BTC) direto pra `wallet` do Minerador sortudo.
   - **Pool Fee**: 5% de taxa de hospedagem do SaaS.
   - **Contributor Reward**: Os 85% restantes da recompensa são divididos utilizando algoritmos de PPLNS (Pay Per Last N Shares) para todos os Mineradores que trabalharam no Puzzle.

---

## 5. Próximos Passos na Codificação (Golang)

1. Adicionar `github.com/gorilla/websocket` ou `google.golang.org/grpc` no nosso projeto.
2. Criar a struct `MiningServer` no `main.go`.
3. Compilar uma segunda versão do app: `go build -o secp256-miner.exe ./cmd/miner`, separando o Core Visual da máquina-cliente da mecânica de comunicação externa.
