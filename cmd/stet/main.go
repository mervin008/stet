// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This binary is the main entrypoint for the STET command line tool.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"syscall"

	"flag"
	"github.com/GoogleCloudPlatform/stet/client"
	configpb "github.com/GoogleCloudPlatform/stet/proto/config_go_proto"
	glog "github.com/golang/glog"
	"github.com/google/subcommands"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"
)

// The default name for the STET configuration file.
const defaultConfigName string = "stet.yaml"

// encryptCmd handles CLI options for the encryption command.
type encryptCmd struct {
	configFile string
	blobID     string
	quiet      bool
}

func (*encryptCmd) Name() string { return "encrypt" }
func (*encryptCmd) Synopsis() string {
	return "encrypts plaintext according to the given config"
}
func (*encryptCmd) Usage() string {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		glog.Errorf("Failed to get config directory location: %v", err.Error())
	}

	return fmt.Sprintf(`Usage: stet encrypt [--config-file=<config_file>] [--blob-id=<blob_id>] <plaintext_file> <encrypted_file>

Examples:
  Encrypt a file using STET, using %s for configuration:
    $ stet encrypt plaintext.txt ciphertext.txt

  Encrypt with the given blob ID and specific configuration file:
    $ stet encrypt --config-file="my_config.yaml" --blob-id="foobar" plaintext.txt ciphertext.txt

  Encrypt with plaintext input from stdin:
    $ stet encrypt - ciphertext.txt < plaintext.txt

	Encrypt with ciphertext output written to stdout:
    $ stet encrypt plaintext.txt - > ciphertext.txt

  Encrypt with input from stdin and output to stdout:
	 $ my-application | stet encrypt - - | my-other-application

Flags:
`, fmt.Sprintf("%s/%s", cfgDir, defaultConfigName))
	// The flags are automatically printed after the returned text.
}
func (e *encryptCmd) SetFlags(f *flag.FlagSet) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		glog.Errorf("Failed to get config directory location: %v", err.Error())
	}

	configFilePath := fmt.Sprintf("%s/%s", cfgDir, defaultConfigName)
	f.StringVar(&e.configFile, "config-file", configFilePath, "Path to a StetConfig YAML file. Optional.")
	f.StringVar(&e.blobID, "blob-id", "", "The blob ID to assign to the encrypted blob. Optional.")
	f.BoolVar(&e.quiet, "quiet", false, "Suppress logging output.")
}

func (e *encryptCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	yamlBytes, err := os.ReadFile(e.configFile)
	if err != nil {
		glog.Errorf("Failed to read config file: %v", err.Error())
		return subcommands.ExitFailure
	}

	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		glog.Errorf("Failed to convert config YAML to JSON: %v", err.Error())
		return subcommands.ExitFailure
	}

	stetConfig := &configpb.StetConfig{}
	if err := protojson.Unmarshal(jsonBytes, stetConfig); err != nil {
		glog.Errorf("Failed to unmarshal StetConfig: %v", err.Error())
		return subcommands.ExitFailure
	}

	if f.NArg() < 2 {
		glog.Errorf("Not enough arguments (expected plaintext file and encrypted file)")
		return subcommands.ExitFailure
	}

	var plaintext []byte

	if f.Arg(0) == "-" {
		// Read input from stdin.
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Split(bufio.ScanBytes)
		for scanner.Scan() {
			plaintext = append(plaintext, scanner.Bytes()...)
		}

		if err := scanner.Err(); err != nil {
			glog.Errorf("Error reading input from stdin: %v", err.Error())
			return subcommands.ExitFailure
		}
	} else {
		plaintext, err = os.ReadFile(f.Arg(0))
		if err != nil {
			glog.Errorf("Failed to read plaintext file: %v", err.Error())
			return subcommands.ExitFailure
		}
	}

	// Initialize StetClient and encrypt plaintext.
	c := client.StetClient{}

	encryptedData, err := c.Encrypt(ctx, plaintext, stetConfig.GetEncryptConfig(), stetConfig.GetAsymmetricKeys(), e.blobID)
	if err != nil {
		glog.Errorf("Failed to encrypt plaintext: %v", err.Error())
		return subcommands.ExitFailure
	}

	marshaled, err := proto.Marshal(encryptedData)
	if err != nil {
		glog.Errorf("Failed to serialize encrypted data: %v", err.Error())
		return subcommands.ExitFailure
	}

	var outFile *os.File
	var logFile *os.File

	if f.Arg(1) == "-" {
		outFile = os.Stdout
		logFile = os.Stderr
	} else {

		outFile, err = os.Create(f.Arg(1))
		if err != nil {
			glog.Errorf("Failed to open file for encrypted data: %v", err.Error())
			return subcommands.ExitFailure
		}
		defer outFile.Close()

		logFile = os.Stdout
	}

	if _, err := outFile.Write(marshaled); err != nil {
		glog.Errorf("Failed to write encrypted data to disk: %v", err.Error())
		return subcommands.ExitFailure
	}

	if !e.quiet {
		logFile.WriteString(fmt.Sprintln("Wrote encrypted data to", outFile.Name()))
	}

	return subcommands.ExitSuccess
}

// decryptCmd handles CLI options for the decryption command.
type decryptCmd struct {
	configFile string
	blobID     string
	quiet      bool
}

func (*decryptCmd) Name() string { return "decrypt" }
func (*decryptCmd) Synopsis() string {
	return "decrypts blob and metadata according to the given config"
}
func (*decryptCmd) Usage() string {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		glog.Errorf("Failed to get config directory location: %v", err.Error())
	}

	return fmt.Sprintf(`Usage: stet decrypt [--config-file=<config_file>] [--blob-id=<blob_id>] <ciphertext_file> <plaintext_file>

Example:
  Decrypt a file using STET, using %s for configuration:
    $ stet decrypt ciphertext.txt plaintext.txt
    Wrote plaintext to plaintext.txt
    Blob ID of decrypted data: ...
    Used these key URIs: [...]

  Decrypt with the given blob ID and a specific configuration file:
    $ stet decrypt --config-file="my_config.yaml" --blob-id="foobar" ciphertext.txt plaintext.txt
    Wrote plaintext to plaintext.txt
    Blob ID of decrypted data: foobar
    Used these key URIs: [...]

  Decrypt with ciphertext input from stdin:
    $ stet decrypt - plaintext.txt < ciphertext.txt
    Wrote plaintext to plaintext.txt
    Blob ID of decrypted data: ...
    Used these key URIs: [...]

	Decrypt with plaintext outputted to stdout:
    $ stet decrypt ciphertext.txt - > plaintext.txt
		Wrote plaintext to stdout.
    Blob ID of decrypted data: ...
    Used these key URIs: [...]

  Decrypt with input from stdin and output to stdout:
	  $ my-application | stet decrypt - - | my-other-application
    Wrote plaintext to stdout.
    Blob ID of decrypted data: ...
    Used these key URIs: [...]

Flags:
`, fmt.Sprintf("%s/%s", cfgDir, defaultConfigName))
}
func (d *decryptCmd) SetFlags(f *flag.FlagSet) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		glog.Errorf("Failed to get config directory location: %v", err.Error())
	}

	configFilePath := fmt.Sprintf("%s/%s", cfgDir, defaultConfigName)
	f.StringVar(&d.configFile, "config-file", configFilePath, "Path to a StetConfig YAML file. Optional.")
	f.StringVar(&d.blobID, "blob-id", "", "The blob ID to validate the decryption against. Optional.")
	f.BoolVar(&d.quiet, "quiet", false, "Suppress logging output.")
}

func (d *decryptCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	yamlBytes, err := os.ReadFile(d.configFile)
	if err != nil {
		glog.Errorf("Failed to read config file: %v", err.Error())
		return subcommands.ExitFailure
	}

	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		glog.Errorf("Failed to convert config YAML to JSON: %v", err.Error())
		return subcommands.ExitFailure
	}

	stetConfig := &configpb.StetConfig{}
	if err := protojson.Unmarshal(jsonBytes, stetConfig); err != nil {
		glog.Errorf("Failed to unmarshal StetConfig: %v", err.Error())
		return subcommands.ExitFailure
	}

	if f.NArg() < 2 {
		glog.Errorf("Not enough arguments (expected encrypted file and plaintext file)")
		return subcommands.ExitFailure
	}

	// Initialize StetClient and decrypt plaintext.
	c := client.StetClient{}

	var encryptedBytes []byte
	if f.Arg(0) == "-" {
		// Read input from stdin.
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Split(bufio.ScanBytes)
		for scanner.Scan() {
			encryptedBytes = append(encryptedBytes, scanner.Bytes()...)
		}

		if err := scanner.Err(); err != nil {
			glog.Errorf("Error reading input from stdin: %v", err.Error())
			return subcommands.ExitFailure
		}
	} else {
		encryptedBytes, err = os.ReadFile(f.Arg(0))
		if err != nil {
			glog.Errorf("Failed to read encrypted data file: %v", err.Error())
			return subcommands.ExitFailure
		}
	}

	encryptedData := &configpb.EncryptedData{}
	if err := proto.Unmarshal(encryptedBytes, encryptedData); err != nil {
		glog.Errorf("Failed to unmarshal encrypted data: %v", err.Error())
		return subcommands.ExitFailure
	}

	decryptedData, err := c.Decrypt(ctx, encryptedData, stetConfig.GetDecryptConfig(), stetConfig.GetAsymmetricKeys())
	if err != nil {
		glog.Errorf("Failed to decrypt ciphertext: %v", err.Error())
		return subcommands.ExitFailure
	}

	var outFile *os.File
	var logFile *os.File

	if f.Arg(1) == "-" {
		// Output to stdout and log to stderr.
		outFile = os.Stdout
		logFile = os.Stderr
	} else {
		outFile, err = os.Create(f.Arg(1))
		if err != nil {
			glog.Errorf("Failed to open file for plaintext: %v", err.Error())
			return subcommands.ExitFailure
		}
		defer outFile.Close()

		logFile = os.Stdout
	}

	if _, err := outFile.Write(decryptedData.Plaintext); err != nil {
		glog.Errorf("Failed to write plaintext to disk: %v", err.Error())
		return subcommands.ExitFailure
	}

	if !d.quiet {
		logFile.WriteString(fmt.Sprintln("Wrote plaintext to", outFile.Name()))

		// Debug information to guard against authorship attacks.
		logFile.WriteString(fmt.Sprintln("Blob ID of decrypted data:", decryptedData.BlobID))
		if len(decryptedData.KeyUris) > 0 {
			logFile.WriteString(fmt.Sprintln("Used these key URIs:", decryptedData.KeyUris))
		}
	}

	return subcommands.ExitSuccess
}

func main() {
	// If effective UID is 0 and real UID != 0, we invoked as user but need to descalate.
	euid := syscall.Geteuid()
	ruid := syscall.Getuid()
	if euid == 0 && ruid != 0 {
		// This means we are root. Swap the real and effective UIDs to de-escalate until
		// we need to re-escalate (as part of generating attestations).
		err := syscall.Setreuid(euid, ruid)
		if err != nil {
			glog.Fatalf("Failed to deescalate from root to user: %s", err.Error())
		}
	}

	flag.Parse()

	subcommands.Register(subcommands.HelpCommand(), "")
	subcommands.Register(&encryptCmd{}, "")
	subcommands.Register(&decryptCmd{}, "")

	ctx := context.Background()
	os.Exit(int(subcommands.Execute(ctx)))
}
