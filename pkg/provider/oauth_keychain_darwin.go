//go:build darwin && cgo

package provider

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>

static CFStringRef oauthCFString(const char *value) {
	return CFStringCreateWithCString(kCFAllocatorDefault, value, kCFStringEncodingUTF8);
}

static OSStatus oauthKeychainRead(const char *service, const char *account, void **bytes, CFIndex *length) {
	CFStringRef serviceValue = oauthCFString(service);
	CFStringRef accountValue = oauthCFString(account);
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecReturnData, kSecMatchLimit };
	const void *values[] = { kSecClassGenericPassword, serviceValue, accountValue, kCFBooleanTrue, kSecMatchLimitOne };
	CFDictionaryRef query = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 5,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFTypeRef result = NULL;
	OSStatus status = SecItemCopyMatching(query, &result);
	if (status == errSecSuccess) {
		CFDataRef data = (CFDataRef)result;
		*length = CFDataGetLength(data);
		*bytes = malloc((size_t)*length);
		if (*length > 0) memcpy(*bytes, CFDataGetBytePtr(data), (size_t)*length);
	}
	if (result) CFRelease(result);
	CFRelease(query);
	CFRelease(accountValue);
	CFRelease(serviceValue);
	return status;
}

static OSStatus oauthKeychainWrite(const char *service, const char *account, const void *bytes, CFIndex length,
	const char *authPath, const char *serverPath) {
	CFStringRef serviceValue = oauthCFString(service);
	CFStringRef accountValue = oauthCFString(account);
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, bytes, length);
	const void *queryKeys[] = { kSecClass, kSecAttrService, kSecAttrAccount };
	const void *queryValues[] = { kSecClassGenericPassword, serviceValue, accountValue };
	CFDictionaryRef query = CFDictionaryCreate(kCFAllocatorDefault, queryKeys, queryValues, 3,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	const void *updateKeys[] = { kSecValueData };
	const void *updateValues[] = { data };
	CFDictionaryRef update = CFDictionaryCreate(kCFAllocatorDefault, updateKeys, updateValues, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	OSStatus status = SecItemUpdate(query, update);
	if (status == errSecItemNotFound) {
		SecTrustedApplicationRef authApp = NULL;
		SecTrustedApplicationRef serverApp = NULL;
		SecAccessRef access = NULL;
		status = SecTrustedApplicationCreateFromPath(authPath, &authApp);
		if (status == errSecSuccess) status = SecTrustedApplicationCreateFromPath(serverPath, &serverApp);
		if (status == errSecSuccess) {
			const void *trustedValues[] = { authApp, serverApp };
			CFArrayRef trusted = CFArrayCreate(kCFAllocatorDefault, trustedValues, 2, &kCFTypeArrayCallBacks);
			status = SecAccessCreate(CFSTR("Slack MCP OAuth"), trusted, &access);
			CFRelease(trusted);
		}
		if (status == errSecSuccess) {
			const void *addKeys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecValueData, kSecAttrAccess };
			const void *addValues[] = { kSecClassGenericPassword, serviceValue, accountValue, data, access };
			CFDictionaryRef item = CFDictionaryCreate(kCFAllocatorDefault, addKeys, addValues, 5,
				&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
			status = SecItemAdd(item, NULL);
			CFRelease(item);
		}
		if (access) CFRelease(access);
		if (serverApp) CFRelease(serverApp);
		if (authApp) CFRelease(authApp);
	}
	CFRelease(update);
	CFRelease(query);
	CFRelease(data);
	CFRelease(accountValue);
	CFRelease(serviceValue);
	return status;
}

static OSStatus oauthKeychainDelete(const char *service, const char *account) {
	CFStringRef serviceValue = oauthCFString(service);
	CFStringRef accountValue = oauthCFString(account);
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount };
	const void *values[] = { kSecClassGenericPassword, serviceValue, accountValue };
	CFDictionaryRef query = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 3,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	CFRelease(accountValue);
	CFRelease(serviceValue);
	return status;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

const keychainItemNotFound = C.OSStatus(-25300)

func platformKeychainCommandRunner() commandRunner {
	return func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name != "security" {
			return nil, errors.New("unsupported Keychain command")
		}
		operation, service, account, err := parseKeychainArgs(args)
		if err != nil {
			return nil, err
		}
		serviceValue := C.CString(service)
		accountValue := C.CString(account)
		defer C.free(unsafe.Pointer(serviceValue))
		defer C.free(unsafe.Pointer(accountValue))

		switch operation {
		case "find-generic-password":
			var raw unsafe.Pointer
			var length C.CFIndex
			status := C.oauthKeychainRead(serviceValue, accountValue, &raw, &length)
			if status == keychainItemNotFound {
				return nil, ErrCredentialNotFound
			}
			if status != 0 {
				return nil, errors.New("Keychain read failed")
			}
			defer C.free(raw)
			return C.GoBytes(raw, C.int(length)), nil
		case "add-generic-password":
			var raw unsafe.Pointer
			if len(stdin) > 0 {
				raw = unsafe.Pointer(&stdin[0])
			}
			executable, err := os.Executable()
			if err != nil {
				return nil, errors.New("resolve Keychain trusted applications")
			}
			binaryDir := filepath.Dir(executable)
			authPath := C.CString(filepath.Join(binaryDir, "slack-mcp-auth"))
			serverPath := C.CString(filepath.Join(binaryDir, "slack-mcp-server"))
			defer C.free(unsafe.Pointer(authPath))
			defer C.free(unsafe.Pointer(serverPath))
			if status := C.oauthKeychainWrite(serviceValue, accountValue, raw, C.CFIndex(len(stdin)), authPath, serverPath); status != 0 {
				return nil, errors.New("Keychain write failed")
			}
			return nil, nil
		case "delete-generic-password":
			status := C.oauthKeychainDelete(serviceValue, accountValue)
			if status == keychainItemNotFound {
				return nil, ErrCredentialNotFound
			}
			if status != 0 {
				return nil, errors.New("Keychain delete failed")
			}
			return nil, nil
		default:
			return nil, errors.New("unsupported Keychain operation")
		}
	}
}

func parseKeychainArgs(args []string) (operation, service, account string, err error) {
	if len(args) == 0 {
		return "", "", "", errors.New("missing Keychain operation")
	}
	operation = args[0]
	for i := 1; i < len(args)-1; i++ {
		switch args[i] {
		case "-s":
			service = args[i+1]
		case "-a":
			account = args[i+1]
		}
	}
	if service == "" || account == "" {
		return "", "", "", fmt.Errorf("missing Keychain item identity")
	}
	return operation, service, account, nil
}
