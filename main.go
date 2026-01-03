package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha1"
	_ "embed" // Embed mekanizması için gerekli
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ascii.txt dosyasını derleme anında string değişkenine gömer
//
//go:embed ascii.txt
var asciiArt string

// Yapılandırma Sabitleri
const (
	APIUrl = "https://client.craftrise.network/api/launcher/hashs.php"
	// Azul Zulu JDK 8 with JavaFX (Linux x64) - ~180MB
	JavaDownloadURL = "https://cdn.azul.com/zulu/bin/zulu8.82.0.21-ca-fx-jdk8.0.432-linux_x64.tar.gz"
	JavaDirName     = "zulu8.82.0.21-ca-fx-jdk8.0.432-linux_x64"
)

// JSON Veri Yapıları
type ServerResponse struct {
	Main      map[string]string `json:"MAIN"`
	WindowsBS WindowsConfig     `json:"WINDOWS_BS"`
}

type WindowsConfig struct {
	JavaURL        string `json:"javaURL"`
	LauncherURL    string `json:"launcherURL"`
	StartArguments string `json:"startArguments"`
}

type LaunchArgs struct {
	ExecutablePath string `json:"executablepath"`
}

func main() {
	// Terminali temizle
	clearCmd := exec.Command("clear")
	clearCmd.Stdout = os.Stdout
	clearCmd.Run()

	// Gömülü ASCII sanatını yazdır
	fmt.Print(asciiArt)

	// 1. Çalışma Dizinini Hazırla (~/.craftrise)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fatal("Ev dizini bulunamadı:", err)
	}
	baseDir := filepath.Join(homeDir, ".craftrise")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		fatal("Klasör oluşturulamadı:", err)
	}
	fmt.Printf("[*] Çalışma dizini: %s\n", baseDir)

	// 2. Taşınabilir Java (Portable Java) Kontrolü
	javaBinPath := filepath.Join(baseDir, "runtime", JavaDirName, "bin", "java")
	
	// Dosya var mı kontrol et
	if _, err := os.Stat(javaBinPath); os.IsNotExist(err) {
		fmt.Println("[*] Uyumlu Java (JavaFX dahil) bulunamadı. İndiriliyor...")
		fmt.Println("    Kaynak: Azul Zulu JDK 8 FX (~100MB+)")
		
		runtimeDir := filepath.Join(baseDir, "runtime")
		os.MkdirAll(runtimeDir, 0755)
		
		tarPath := filepath.Join(runtimeDir, "java_runtime.tar.gz")
		if err := downloadFileWithProgress(tarPath, JavaDownloadURL); err != nil {
			fatal("Java indirilemedi:", err)
		}
		
		fmt.Println("[*] Java kurulumu yapılıyor (Arşivden çıkarılıyor)...")
		if err := extractTarGz(tarPath, runtimeDir); err != nil {
			fatal("Java arşivden çıkarılamadı:", err)
		}
		os.Remove(tarPath) // İndirilen arşivi sil
		fmt.Println("[+] Java başarıyla kuruldu.")
	} else {
		fmt.Println("[+] Uyumlu Java mevcut.")
	}

	// Çalıştırma izni ver
	os.Chmod(javaBinPath, 0755)

	// 3. Sunucu Konfigürasyonunu Çek
	fmt.Println("[*] Sunucu verileri alınıyor...")
	config, err := fetchConfig()
	if err != nil {
		fmt.Println("[!] Sunucuya erişilemedi. Cache veya varsayılanlar kullanılacak.")
		config = &ServerResponse{}
	}

	// 4. Launcher Güncelleme Kontrolü
	launcherPath := filepath.Join(baseDir, "launcher.jar")
	remoteHash := config.Main["launcher.jar"]
	localHash := getFileHash(launcherPath)

	if remoteHash != "" && localHash != remoteHash {
		fmt.Println("[*] Launcher güncellemesi mevcut. İndiriliyor...")
		downloadURL := config.WindowsBS.LauncherURL
		if downloadURL == "" {
			downloadURL = "https://client.craftrise.network/api/launcher/files/" + remoteHash + ".jar"
		}
		
		if err := downloadFileWithProgress(launcherPath, downloadURL); err != nil {
			fatal("Launcher indirilemedi:", err)
		}
		fmt.Println("[+] Launcher güncellendi.")
	} else {
		fmt.Println("[+] Launcher güncel.")
	}

	// 5. Başlatma Argümanlarını Hazırla
	memory := "2048" // Varsayılan 2GB
	argsTemplate := config.WindowsBS.StartArguments
	if argsTemplate == "" {
		argsTemplate = "-Xmx%selectedRAM%M -Djava.net.preferIPv4Stack=true -XX:+DisableAttachMechanism -Dcom.ibm.tools.attach.enable=no -Xmn128M"
	}
	
	jvmArgsStr := strings.ReplaceAll(argsTemplate, "%selectedRAM%", memory)
	jvmArgs := strings.Fields(jvmArgsStr)

	// Özel şifreli argüman
	exPath, _ := os.Executable()
	launchObj := LaunchArgs{ExecutablePath: exPath}
	jsonBytes, _ := json.Marshal(launchObj)
	encodedArgs := base64.StdEncoding.EncodeToString(jsonBytes)

	// Komutu oluştur
	finalArgs := append(jvmArgs, "-jar", launcherPath, "launcherStartup", encodedArgs)
	
	// Sahte wmic oluştur (Linux'ta Windows komutu arayan launcher için)
	binDir := filepath.Join(baseDir, "bin")
	os.MkdirAll(binDir, 0755)
	wmicPath := filepath.Join(binDir, "wmic")
	os.WriteFile(wmicPath, []byte("#!/bin/sh\nexit 0"), 0755)

	fmt.Println("[*] CraftRise başlatılıyor...")
	
	cmd := exec.Command(javaBinPath, finalArgs...)
	cmd.Dir = baseDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	// PATH'e sahte bin klasörünü ekle
	newPath := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	cmd.Env = append(os.Environ(), "PATH="+newPath)

	if err := cmd.Start(); err != nil {
		fatal("Başlatma hatası:", err)
	}

	fmt.Println("[+] Oyun süreci başlatıldı. (PID:", cmd.Process.Pid, ")")
	if err := cmd.Wait(); err != nil {
		fmt.Println("\n[!] Oyun kapandı:", err)
	}
}

func fatal(msg string, err error) {
	fmt.Printf("[ERROR] %s %v\n", msg, err)
	os.Exit(1)
}

func fetchConfig() (*ServerResponse, error) {
	resp, err := http.Get(APIUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var data ServerResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func getFileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func downloadFileWithProgress(filepath string, url string) error {
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	size := resp.ContentLength
	done := int64(0)
	buf := make([]byte, 32*1024)
	
	for {
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			break
		}
		
		if _, err := out.Write(buf[:n]); err != nil {
			return err
		}
		
		done += int64(n)
		if size > 0 {
			// fmt.Printf("\r    İndiriliyor... %% %.1f", float64(done)/float64(size)*100)
		}
	}
	fmt.Println("\r    İndirme tamamlandı.")
	return nil
}

func extractTarGz(path string, dest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	uncompressedStream, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer uncompressedStream.Close()

	tarReader := tar.NewReader(uncompressedStream)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}
	return nil
}