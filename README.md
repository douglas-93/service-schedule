# Service Schedule

Utilitário em Go para parar e iniciar serviços do Windows de forma imediata ou em horários programados. Pode substituir o Task Scheduler quando ele não estiver disponível ou não for confiável.

## Requisitos

- Windows, para executar o programa.
- Go 1.27 ou superior, para compilação.
- Permissão administrativa para controlar os serviços do Windows.
- Os nomes exatos dos serviços que serão controlados.

## Instalação e compilação

Clone o projeto e baixe as dependências:

```bash
go mod download
```

Compile para Windows 64 bits:

```bash
GOOS=windows GOARCH=amd64 go build -o service-schedule.exe .
```

No PowerShell, as variáveis de ambiente podem ser definidas assim:

```powershell
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -o service-schedule.exe .
```

## Uso

O parâmetro obrigatório é `-services`, com os nomes dos serviços separados por vírgula.

### Execução única

Para os serviços, aguarda o intervalo definido e inicia-os novamente:

```powershell
.\service-schedule.exe -mode once -services "Spooler,W3SVC" -delay 5m
```

### Agendador contínuo

Mantém o programa em execução e, todos os dias, para e inicia os serviços nos horários definidos:

```powershell
.\service-schedule.exe -mode daemon -services "Spooler,W3SVC" -stop-at 22:00 -start-at 06:00
```

O programa pode ser interrompido com `Ctrl+C` ou com um sinal de encerramento. Os serviços são controlados em paralelo.

### Servidor remoto

Para controlar serviços em outro computador, informe um endereço IP válido:

```powershell
.\service-schedule.exe -mode once -services "Spooler" -host 192.168.1.100 -delay 30s
```

A máquina que executa o programa precisa ter conectividade e permissões suficientes para acessar o Service Control Manager remoto.

## Configuração com `.env`

Copie o arquivo `.env.example` para `.env` e ajuste os valores:

```powershell
Copy-Item .env.example .env
```

Exemplo:

```dotenv
MODE=daemon
SERVICES=Spooler,W3SVC,MSSQLSERVER
DELAY=5m
STOP_AT=22:00
START_AT=06:00
CTL_TIMEOUT=30s
REMOTE_HOST=192.168.1.100
```

Use outro arquivo de configuração com `-env`:

```powershell
.\service-schedule.exe -env .\producao.env
```

A precedência dos valores é:

1. Flag informada na linha de comando.
2. Variável de ambiente já definida no sistema.
3. Valor encontrado no arquivo `.env`.
4. Valor padrão da flag.

## Parâmetros

| Parâmetro | Padrão | Descrição |
| --- | --- | --- |
| `-env` | `.env` | Caminho do arquivo de ambiente usado como fallback. |
| `-mode` | `once` | Modo de execução: `once` ou `daemon`. |
| `-services` | vazio | Serviços separados por vírgula. Obrigatório. |
| `-host` | vazio | Endereço IP remoto; vazio executa localmente. |
| `-delay` | `5m` | Tempo entre parar e iniciar no modo `once`. |
| `-stop-at` | `22:00` | Horário diário para parar no modo `daemon`. |
| `-start-at` | `06:00` | Horário diário para iniciar no modo `daemon`. |
| `-ctl-timeout` | `30s` | Tempo máximo para confirmar cada operação de Start/Stop. |

Veja todos os parâmetros com:

```powershell
.\service-schedule.exe -help
```

## Desenvolvimento

Formate e compile o projeto com:

```bash
gofmt -w main.go
go build .
```

O arquivo `.env` e os executáveis gerados são ignorados pelo Git. Use `.env.example` como modelo de configuração sem incluir dados específicos do ambiente no repositório.
