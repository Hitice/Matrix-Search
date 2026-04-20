# SECP256 Key Worker Dashboard

Um scanner de alta performance para o puzzle do Bitcoin (SECP256k1), apresentando um motor GPU em tempo real e um dashboard web profissional para telemetria, rastreamento de progresso e detecção de chaves (hits).

![Dashboard Monitoramento](docs/screenshots/monitor.png)

## Funcionalidades
- **Motor GPU Acelerado**: Núcleo otimizado em CUDA/C++ para mapeamento rápido de pontos SECP256k1.
- **Telemetria em Tempo Real**: Monitore o throughput (MKeys/s), consumo de energia da GPU e progresso da busca.
- **Persistência Robusta**: Checkpointing e retomada automática — nunca perca o progresso da varredura.
- **Dashboard Web**: Interface moderna com tema escuro para implantação de alvos e monitoramento do console.

![Histórico de Chaves](docs/screenshots/history.png)

## Pré-requisitos
- **GPU NVIDIA**: Arquitetura Maxwell ou mais recente recomendada.
- **CUDA Toolkit**: Versão 11.0+ (Testado na v13.1).
- **Golang**: Versão 1.20+ para o Master Controller e API Server (substituindo o antigo Python).
- **Compilador MSVC**: Necessário para compilar o núcleo CUDA no Windows.

## Instalação

1. **Clone o repositório**:
   ```bash
   git clone https://github.com/seuusuario/secp256-key-worker.git
   cd secp256-key-worker
   ```

2. **Compile o Motor GPU**:
   Certifique-se de que o `nvcc` está no seu PATH. Execute o seguinte comando (ajuste de acordo com a versão do seu compilador, se necessário):
   ```bash
   nvcc -O3 kangaroo.cu -o kangaroo.exe
   ```

3. **Compile o Orquestrador**:
   ```bash
   go mod download
   go build -o secp256-master.exe
   ```

4. **Configure o alvo**:
   Copie a configuração de exemplo e atualize-a.
   ```bash
   copy current_target.json.example current_target.json
   ```

## Uso

Inicie o Core Master:
```bash
.\secp256-master.exe
```

Acesse o dashboard em: `http://localhost:8080`

## Estrutura do Projeto
- `main.go`: Orquestrador monolítico de altíssima performance englobando Web Server, Gestão de Memória e Processamento de Colisão. Substitui inteiramente os antigos orquestradores em Python.
- `kangaroo.cu`: Código fonte CUDA puro para processamento em larga escala na GPU.
- `dashboard.html`: Console web responsivo em Single-Page.

## Licença
Este projeto está licenciado sob a Licença MIT - veja o arquivo LICENSE para detalhes.

## Aviso Legal
Este software é fornecido apenas para fins educacionais. O uso desta ferramenta para pesquisa criptográfica é de inteira responsabilidade do usuário.
