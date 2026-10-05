/*
Utilitário em Go para gerenciar (parar/iniciar) serviços do Windows,
substituindo o Task Scheduler quando ele não está funcionando.

Requer o módulo oficial da extensão Windows do Go:

	go get golang.org/x/sys/windows/svc/mgr

Compilação (a partir de Linux/Mac, cross-compile para Windows):

	GOOS=windows GOARCH=amd64 go build -o svcsched.exe service_scheduler.go

---------------------------------------------------------------------------
MODO 1 - one-shot: para agora, espera X tempo, reinicia e encerra.

	svcsched.exe -mode once -services "Spooler,MyService" -delay 5m

MODO 2 - agendador contínuo: fica rodando (ideal como serviço do Windows)
e todo dia, nos horários definidos, para e inicia os serviços.

	svcsched.exe -mode daemon -services "Spooler,MyService" -stop-at 22:00 -start-at 06:00

---------------------------------------------------------------------------
SUPORTE A .env

As flags continuam funcionando normalmente e têm prioridade máxima.
Se uma flag NÃO for passada na linha de comando, o programa procura o
valor correspondente em um arquivo .env (por padrão, na pasta atual;
mude com -env caminho\para\arquivo.env). Chaves aceitas no .env:

	SERVICES=Spooler,MyService
	MODE=daemon
	DELAY=5m
	STOP_AT=22:00
	START_AT=06:00
	CTL_TIMEOUT=30s
	START_RETRIES=3
	START_RETRY_INTERVAL=5s
	REMOTE_HOST=192.168.1.100

Ordem de prioridade: flag explícita > variável de ambiente já exportada
no sistema > valor do .env > valor padrão da flag.
---------------------------------------------------------------------------
*/
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// applyEnvFallback aplica, no map de env do processo, as chaves do .env
// que ainda não existirem como variável de ambiente real (env real sempre vence).
func applyEnvFallback(envFile map[string]string) {
	for k, v := range envFile {
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}

// connectSCM abre a conexão local ou remota, dependendo se host foi informado.
func connectSCM(host string) (*mgr.Mgr, error) {
	if host != "" {
		return mgr.ConnectRemote(host)
	}
	return mgr.Connect()
}

// ----------------------- Funções de controle de serviço -----------------------

func stopServiceConcurrently(host, name string, timeout time.Duration) error {
	m, err := connectSCM(host)
	if err != nil {
		return fmt.Errorf("conectar ao SCM para %q: %w", name, err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("abrir serviço %q: %w", name, err)
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("consultar estado de %q: %w", name, err)
	}
	if status.State == svc.Stopped {
		log.Printf("[%s] já estava parado", name)
		return nil
	}

	status, err = s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("enviar Stop para %q: %w", name, err)
	}

	deadline := time.Now().Add(timeout)
	for status.State != svc.Stopped {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout esperando %q parar (estado atual: %d)", name, status.State)
		}
		time.Sleep(500 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("consultar estado de %q: %w", name, err)
		}
	}
	log.Printf("[%s] parado com sucesso", name)
	return nil
}

func startServiceConcurrently(host, name string, timeout time.Duration) error {
	m, err := connectSCM(host)
	if err != nil {
		return fmt.Errorf("conectar ao SCM para %q: %w", name, err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("abrir serviço %q: %w", name, err)
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("consultar estado de %q: %w", name, err)
	}
	if status.State == svc.Running {
		log.Printf("[%s] já estava rodando", name)
		return nil
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("enviar Start para %q: %w", name, err)
	}

	deadline := time.Now().Add(timeout)
	for status.State != svc.Running {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout esperando %q iniciar (estado atual: %d)", name, status.State)
		}
		time.Sleep(500 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("consultar estado de %q: %w", name, err)
		}
	}
	log.Printf("[%s] iniciado com sucesso", name)
	return nil
}

func retryStart(name string, retries int, retryDelay time.Duration, start func() error) error {
	for attempt := 1; ; attempt++ {
		err := start()
		if err == nil {
			return nil
		}
		if attempt > retries {
			return fmt.Errorf("falha ao iniciar %q após %d tentativa(s): %w", name, attempt, err)
		}
		log.Printf("ERRO ao iniciar %q (tentativa %d): %v; nova tentativa em %s", name, attempt, err, retryDelay)
		time.Sleep(retryDelay)
	}
}

func stopAll(host string, names []string, timeout time.Duration) {
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(serviceName string) {
			defer wg.Done()
			if err := stopServiceConcurrently(host, serviceName, timeout); err != nil {
				log.Printf("ERRO ao parar %q: %v", serviceName, err)
			}
		}(n)
	}
	wg.Wait()
}

func startAll(host string, names []string, timeout, retryInterval time.Duration, retries int) {
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(serviceName string) {
			defer wg.Done()
			if err := retryStart(serviceName, retries, retryInterval, func() error {
				return startServiceConcurrently(host, serviceName, timeout)
			}); err != nil {
				log.Printf("ERRO ao iniciar %q: %v", serviceName, err)
			}
		}(n)
	}
	wg.Wait()
}

// ----------------------------- Modo agendador -----------------------------

func nextOccurrence(from time.Time, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, fmt.Errorf("horário inválido %q (use HH:MM): %w", hhmm, err)
	}
	candidate := time.Date(from.Year(), from.Month(), from.Day(), t.Hour(), t.Minute(), 0, 0, from.Location())
	if !candidate.After(from) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate, nil
}

func runDaemon(host string, names []string, stopAt, startAt string, ctlTimeout time.Duration, startRetries int, retryInterval time.Duration, ctxStop chan struct{}) {
	target := "localhost"
	if host != "" {
		target = host
	}
	log.Printf("Agendador iniciado em modo daemon no host [%s]. Parar às %s, iniciar às %s. Serviços: %s", target, stopAt, startAt, strings.Join(names, ", "))
	for {
		now := time.Now()

		nextStop, err := nextOccurrence(now, stopAt)
		if err != nil {
			log.Fatal(err)
		}
		nextStart, err := nextOccurrence(now, startAt)
		if err != nil {
			log.Fatal(err)
		}

		var targetTime time.Time
		var isStopAction bool

		if nextStop.Before(nextStart) {
			targetTime = nextStop
			isStopAction = true
		} else {
			targetTime = nextStart
			isStopAction = false
		}

		durationToWait := time.Until(targetTime)
		actionText := "iniciar"
		if isStopAction {
			actionText = "parar"
		}
		log.Printf("Aguardando até %s para %s os serviços...", targetTime.Format("2006-01-02 15:04:05"), actionText)

		timer := time.NewTimer(durationToWait)
		select {
		case <-ctxStop:
			timer.Stop()
			log.Println("Daemon encerrando graciosamente...")
			return
		case <-timer.C:
			if isStopAction {
				stopAll(host, names, ctlTimeout)
			} else {
				startAll(host, names, ctlTimeout, retryInterval, startRetries)
			}
		}
	}
}

// -------------------------------- main --------------------------------

func main() {
	// Sobrescreve a flag -help para exibir exatamente o comentário original
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Utilitário em Go para gerenciar (parar/iniciar) serviços do Windows,
alternativa o Task Scheduler.

---------------------------------------------------------------------------
MODO 1 - one-shot: para agora, espera X tempo, reinicia e encerra.
  service-schedule.exe -mode once -services "Spooler,MyService" -delay 5m

MODO 2 - agendador contínuo: fica rodando (ideal como serviço do Windows)
e todo dia, nos horários definidos, para e inicia os serviços.
  service-schedule.exe -mode daemon -services "Spooler,MyService" -stop-at 22:00 -start-at 06:00

---------------------------------------------------------------------------
SUPORTE A .env

As flags têm prioridade sob o .env.
Se uma flag NÃO for passada na linha de comando, o programa procura o
valor correspondente em um arquivo .env (por padrão, na pasta atual;
mude com -env caminho\para\arquivo.env). Chaves aceitas no .env:

  SERVICES=Spooler,MyService
  MODE=daemon
  DELAY=5m
  STOP_AT=22:00
  START_AT=06:00
  CTL_TIMEOUT=30s
  START_RETRIES=3
  START_RETRY_INTERVAL=5s
  REMOTE_HOST=192.168.1.100

Ordem de prioridade: flag explícita > variável de ambiente já exportada
no sistema > valor do .env > valor padrão da flag.
---------------------------------------------------------------------------
`)
	}

	envPath := flag.String("env", ".env", "caminho do arquivo .env usado como fallback para as flags não informadas")
	mode := flag.String("mode", "once", `"once" (para, espera, inicia e sai) ou "daemon" (loop contínuo por horário)`)
	servicesFlag := flag.String("services", "", "nomes dos serviços separados por vírgula, ex: Spooler,MyService")
	hostFlag := flag.String("host", "", "IP do servidor remoto (deixe em branco para local)")
	delay := flag.Duration("delay", 5*time.Minute, "modo once: tempo de espera entre parar e reiniciar (ex: 5m, 30s, 1h)")
	stopAt := flag.String("stop-at", "22:00", "modo daemon: horário HH:MM para parar os serviços")
	startAt := flag.String("start-at", "06:00", "modo daemon: horário HH:MM para iniciar os serviços")
	ctlTimeout := flag.Duration("ctl-timeout", 30*time.Second, "timeout esperando cada Start/Stop confirmar")
	startRetries := flag.Int("start-retries", 3, "quantidade de novas tentativas para iniciar cada serviço")
	retryInterval := flag.Duration("start-retry-interval", 5*time.Second, "intervalo entre tentativas de inicialização (ex: 5s, 1m)")
	flag.Parse()

	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	// Leitura com godotenv
	envMap, err := godotenv.Read(*envPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Aviso: erro ao ler arquivo %q: %v", *envPath, err)
	}
	applyEnvFallback(envMap)

	if !explicit["mode"] {
		if v, ok := os.LookupEnv("MODE"); ok {
			*mode = v
		}
	}
	if !explicit["services"] {
		if v, ok := os.LookupEnv("SERVICES"); ok {
			*servicesFlag = v
		}
	}
	if !explicit["host"] {
		if v, ok := os.LookupEnv("REMOTE_HOST"); ok {
			*hostFlag = v
		}
	}
	if !explicit["delay"] {
		if v, ok := os.LookupEnv("DELAY"); ok {
			d, err := time.ParseDuration(v)
			if err != nil {
				log.Fatalf("DELAY inválido no .env (%q): %v", v, err)
			}
			*delay = d
		}
	}
	if !explicit["stop-at"] {
		if v, ok := os.LookupEnv("STOP_AT"); ok {
			*stopAt = v
		}
	}
	if !explicit["start-at"] {
		if v, ok := os.LookupEnv("START_AT"); ok {
			*startAt = v
		}
	}
	if !explicit["ctl-timeout"] {
		if v, ok := os.LookupEnv("CTL_TIMEOUT"); ok {
			d, err := time.ParseDuration(v)
			if err != nil {
				log.Fatalf("CTL_TIMEOUT inválido no .env (%q): %v", v, err)
			}
			*ctlTimeout = d
		}
	}
	if !explicit["start-retries"] {
		if v, ok := os.LookupEnv("START_RETRIES"); ok {
			retries, err := strconv.Atoi(v)
			if err != nil {
				log.Fatalf("START_RETRIES inválido no .env (%q): deve ser um inteiro não negativo", v)
			}
			*startRetries = retries
		}
	}
	if *startRetries < 0 {
		log.Fatal("START_RETRIES/-start-retries deve ser um inteiro não negativo")
	}
	if !explicit["start-retry-interval"] {
		if v, ok := os.LookupEnv("START_RETRY_INTERVAL"); ok {
			interval, err := time.ParseDuration(v)
			if err != nil {
				log.Fatalf("START_RETRY_INTERVAL inválido no .env (%q): deve ser uma duração válida (ex: 5s, 1m)", v)
			}
			*retryInterval = interval
		}
	}
	if *retryInterval < 0 {
		log.Fatal("START_RETRY_INTERVAL/-start-retry-interval deve ser uma duração não negativa")
	}

	// Validação de IP, se fornecido
	if *hostFlag != "" {
		if net.ParseIP(*hostFlag) == nil {
			log.Fatalf("ERRO: O host fornecido '%s' não é um endereço IP válido. Execução cancelada.", *hostFlag)
		}
	}

	if *servicesFlag == "" {
		log.Fatal("informe -services \"Nome1,Nome2\" ou defina SERVICES no .env. Use -help para mais detalhes.")
	}
	names := strings.Split(*servicesFlag, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	daemonStopChan := make(chan struct{})

	go func() {
		sig := <-sigChan
		log.Printf("Sinal de encerramento recebido (%v). Finalizando...", sig)
		close(daemonStopChan)
	}()

	switch *mode {
	case "once":
		target := "localhost"
		if *hostFlag != "" {
			target = *hostFlag
		}
		log.Printf("Executando em [%s]", target)
		log.Printf("Parando serviços: %s", strings.Join(names, ", "))
		stopAll(*hostFlag, names, *ctlTimeout)

		log.Printf("Aguardando %s antes de reiniciar...", delay.String())

		timer := time.NewTimer(*delay)
		select {
		case <-daemonStopChan:
			timer.Stop()
			log.Println("Execução cancelada durante o delay.")
			return
		case <-timer.C:
			log.Printf("Iniciando serviços: %s", strings.Join(names, ", "))
			startAll(*hostFlag, names, *ctlTimeout, *retryInterval, *startRetries)
		}

	case "daemon":
		go runDaemon(*hostFlag, names, *stopAt, *startAt, *ctlTimeout, *startRetries, *retryInterval, daemonStopChan)
		<-daemonStopChan
		log.Println("Aplicação encerrada com sucesso.")

	default:
		log.Fatalf("modo desconhecido: %s (use once ou daemon)", *mode)
	}
}
