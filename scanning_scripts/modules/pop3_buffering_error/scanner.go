package pop3_buffering_error

import (
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/zmap/zgrab2"
)

// ScanResults instances are returned by the module's Scan function.
type ScanResults struct {

	// Vulnerable is the final result, whether the scan believes the target to be vulnerable
	Vulnerable bool `json:"vulnerable"`

	// Trace is the complete communication between client and server
	Trace []string `json:"trace"`

	// Status is the step, the execution of the Scan ended in (used for deubg)
	Status int `json:"status,omitempty"`

	// TLSLog is the standard TLS log
	TLSLog *zgrab2.TLSLog `json:"tls,omitempty"`
}

// Flags holds the command-line configuration for the IMAP scan module.
// Populated by the framework.
type Flags struct {
	zgrab2.BaseFlags
	zgrab2.TLSFlags

	// Verbose indicates that there should be more verbose logging.
	Verbose bool `short:"v" long:"verbose" description:"More verbose logging, include debug fields in the scan results"`
}

// Module implements the zgrab2.Module interface.
type Module struct {
}

// Scanner implements the zgrab2.Scanner interface.
type Scanner struct {
	config *Flags
}

// RegisterModule registers the zgrab2 module.
func RegisterModule() {
	var module Module
	_, err := zgrab2.AddCommand("pop3_buffering_error", "test for starttls buffering bug in pop3", module.Description(), 110, &module)
	if err != nil {
		log.Fatal(err)
	}
}

// NewFlags returns a default Flags object.
func (module *Module) NewFlags() interface{} {
	return new(Flags)
}

// NewScanner returns a new Scanner instance.
func (module *Module) NewScanner() zgrab2.Scanner {
	return new(Scanner)
}

// Description returns an overview of this module.
func (module *Module) Description() string {
	return "Checks an POP3 Host for the STARTTLS-Buffering-Error"
}

// Validate checks that the flags are valid.
// On success, returns nil.
// On failure, returns an error instance describing the error.
func (flags *Flags) Validate(args []string) error {
	return nil
}

// Help returns the module's help string.
func (flags *Flags) Help() string {
	return "todo"
}

// Init initializes the Scanner.
func (scanner *Scanner) Init(flags zgrab2.ScanFlags) error {
	f, _ := flags.(*Flags)
	scanner.config = f
	return nil
}

// InitPerSender initializes the scanner for a given sender.
func (scanner *Scanner) InitPerSender(senderID int) error {
	return nil
}

// GetName returns the Scanner name defined in the Flags.
func (scanner *Scanner) GetName() string {
	return scanner.config.Name
}

// GetTrigger returns the Trigger defined in the Flags.
func (scanner *Scanner) GetTrigger() string {
	return scanner.config.Trigger
}

// Protocol returns the protocol identifier of the scan.
func (scanner *Scanner) Protocol() string {
	return "pop3"
}

// Scan performs the IMAP scan.
func (scanner *Scanner) Scan(target zgrab2.ScanTarget) (zgrab2.ScanStatus, interface{}, error) {

	result := &ScanResults{}
	singleReadTimeout := time.Second * 5

	// Step 0
	// Connect to Target
	c, err := target.Open(&scanner.config.BaseFlags)
	if err != nil {
		return zgrab2.TryGetScanStatus(err), nil, err
	}
	defer c.Close()
	conn := Connection{Conn: c}
	result.Status = 0

	var ret string
	var command string

	// Read greeting
	conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
	ret, err = conn.ReadResponse()
	if err != nil {
		status := zgrab2.TryGetScanStatus(err)
		return status, result, err
	}
	result.Trace = append(result.Trace, "S: "+strings.Trim(ret, "\r\n"))
	if !strings.Contains(strings.ToUpper(ret), "+OK") && !strings.Contains(strings.ToUpper(ret), "-ERR") {
		status := zgrab2.ScanStatus("no-pop3")
		return status, result, err
	}

	// Step 2
	// Send STLS + CAPA command, Read a single reponse (expecting the EHLO to get buffered into the TLS-Session)
	command = "STLS\r\nCAPA"
	result.Trace = append(result.Trace, "C: "+command)
	conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
	ret, err = conn.SendCommand(command)
	if err != nil {
		return zgrab2.TryGetScanStatus(err), result, err
	}
	result.Trace = append(result.Trace, "S: "+strings.Trim(ret, "\r\n"))
	result.Status++

	// We now expect either:
	// 1. "+OK Begin TLS negotiation" if the target accepts to start tls
	// 2. -ERR ...
	// 3. Connection close
	// 4. TLS Alerts

	// Quit if target didnt "OK" the STARTTLS
	if strings.Contains(strings.ToUpper(ret), "-ERR") || !strings.Contains(strings.ToUpper(ret), "+OK") {

		result.Vulnerable = false
		command = "QUIT"
		result.Trace = append(result.Trace, "C: "+command)
		conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
		ret, err = conn.SendCommand(command)
		if err != nil {
			// Some Servers don't properly terminate the QUIT -> we ignore that
			if err.Error() != "EOF" {
				return zgrab2.TryGetScanStatus(err), result, err
			}
		}
		result.Trace = append(result.Trace, "S: "+strings.Trim(ret, "\r\n"))
		return zgrab2.SCAN_SUCCESS, result, nil
	}

	// Step 3
	// TLS - Handshake
	result.Trace = append(result.Trace, "-- TLS Handshake --")
	conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
	tlsConn, err := scanner.config.TLSFlags.GetTLSConnection(conn.Conn)
	if err != nil {
		return zgrab2.TryGetScanStatus(err), result, err
	}
	// Log TLS details if set on verbose
	if scanner.config.Verbose {
		result.TLSLog = tlsConn.GetLog()
	}
	if err = tlsConn.Handshake(); err != nil {
		status := zgrab2.TryGetScanStatus(err)
		// if the handshake fails due to EOF or timeout, we interpret that as case 3/4 of the above
		if err.Error() == "EOF" {
			result.Vulnerable = false
			status = zgrab2.ScanStatus("EOF in TLS Handshake")
			return status, result, nil
		}
		return status, result, err
	}
	conn.Conn = tlsConn
	result.Status++

	// We now believe to be in case 1 of the above

	// Step 4
	// Try to read the response to the (maybe-buffered) CAPA
	conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
	ret, err = conn.ReadResponse()
	if err != nil {
		result.Vulnerable = false
		status := zgrab2.TryGetScanStatus(err)
		// We are prepared for that not to happen -> only abort on errors different than "got no response"
		if status != zgrab2.SCAN_IO_TIMEOUT {
			return status, result, err
		}
	} else {
		result.Trace = append(result.Trace, "S: "+strings.Trim(ret, "\r\n"))
		// check whether the buffered CAPA was OK'd/ERR'd
		if strings.Contains(strings.ToUpper(ret), "+OK") || strings.Contains(strings.ToUpper(ret), "-ERR") {
			// We are now certain that the target (errornously) buffered the pre-TLS EHLO
			result.Vulnerable = true
		} else {
			return zgrab2.SCAN_UNKNOWN_ERROR, result, nil
		}
	}
	result.Status++

	// Step 5
	// Send a QUIT, maybe giving the errornous buffer a little push
	command = "QUIT"
	result.Trace = append(result.Trace, "C: "+command)
	conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
	ret, err = conn.SendCommand(command)
	if err != nil {
		// Some servers don't properly terminate the last message -> no reason to panic
		if err.Error() != "EOF" {
			return zgrab2.TryGetScanStatus(err), result, err
		}
	}
	result.Trace = append(result.Trace, "S: "+strings.Trim(ret, "\r\n"))
	result.Status++

	// Step 6
	// Read possible further responses until close/timeout
	conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
	for ret, err = conn.ReadResponse(); err == nil; ret, err = conn.ReadResponse() {
		result.Trace = append(result.Trace, "S: "+strings.Trim(ret, "\r\n"))
		// Such responses are interpreted as a vulnerability
		result.Vulnerable = true
		conn.Conn.SetReadDeadline(time.Now().Add(singleReadTimeout))
	}
	result.Status++

	return zgrab2.SCAN_SUCCESS, result, nil
}
