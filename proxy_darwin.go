//go:build darwin && cgo

// Package main — torview: minimal Tor webview browser.
//
// proxy_darwin.go: WKWebView proxy configuration (macOS 14+).
//
// REQUIRES cgo (Objective-C runtime access): build on a Mac (or any host
// with an Apple cross-SDK). With CGO_ENABLED=0 the proxy_darwin_nocgo.go
// stub takes over and refuses to boot with a clear message instead of
// silently routing DIRECT.
//
// The documented API is WKWebsiteDataStore.proxyConfigurations with
// ProxyConfiguration.init(connectToSOCKSv5Proxy:) — macOS 14.0+ / iOS 17.0+
// only. Older SDKs do not declare those selectors, so the webview library's
// prebuilt SDK would fail to build. Solution: resolve the selectors AT RUNTIME
// through the Objective-C runtime. Behavior unchanged, source unbreakable.
//
// This uses WebKit's own networking stack (CFNetwork with its own DNS called
// directly per docs)... rather it does NOT: with proxyConfigurations the
// WebView sends the HOSTNAME to the SOCKSv5 proxy (remote resolution at the
// proxy), which is the property we need to avoid DNS leaks.
package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework WebKit

#import <Foundation/Foundation.h>
#import <objc/runtime.h>
#import <objc/message.h>

// Forward declaration: tvSetSocksProxy calls tvApplyProxyConfig defined
// below. Newer Apple toolchains (Xcode 14+) reject implicit function
// declarations as errors — the prototype must come first.
int tvApplyProxyConfig(id cfg);

// Returns 1 if the runtime knows the proxyConfigurations selectors
// (runtime = macOS 14+ / iOS 17+ SDK or newer runtime), 0 otherwise.
int tvRuntimeHasProxyAPI(void) {
    Class dsClass = objc_getClass("WKWebsiteDataStore");
    Class pcClass  = objc_getClass("ProxyConfiguration");
    if (!dsClass || !pcClass) return 0;
    SEL sel = sel_registerName("proxyConfigurations");
    if (!dsClass || !class_getInstanceMethod(object_getClass((id)dsClass), sel)) {
        // need the instance method on a data store object, tested below
    }
    (void)sel;
    // The real test is done at call time; presence of the ProxyConfiguration
    // factory method is the strongest cheap signal.
    SEL factory = sel_registerName("connectToSOCKSv5Proxy:port:");
    if (class_getClassMethod(pcClass, factory)) return 1;
    // Some SDKs expose connectToSOCKSv5Proxy: (port defaulting) too.
    SEL factory2 = sel_registerName("connectToSOCKSv5Proxy:");
    if (class_getClassMethod(pcClass, factory2)) return 1;
    return 0;
}

// Sets the SOCKSv5 proxy on the default data store. Returns error codes:
//   0  OK
//   1  runtime too old (no ProxyConfiguration class/factory)
//   2  data store rejected the configuration (exception caught)
int tvSetSocksProxy(const char *host, int port) {
    @autoreleasepool {
        Class pcClass = objc_getClass("ProxyConfiguration");
        if (!pcClass) return 1;
        SEL factory = sel_registerName("connectToSOCKSv5Proxy:port:");
        if (!class_getClassMethod(pcClass, factory)) {
            factory = sel_registerName("connectToSOCKSv5Proxy:");
            if (!class_getClassMethod(pcClass, factory)) {
                return 1; // macOS < 14 runtime
            }
            id cfg = ((id(*)(id, SEL, NSString *))objc_msgSend)(
                (id)pcClass, factory, [NSString stringWithUTF8String:host]);
            if (!cfg) return 2;
            return tvApplyProxyConfig(cfg);
        }
        id cfg = ((id(*)(id, SEL, NSString *, int))objc_msgSend)(
            (id)pcClass, factory,
            [NSString stringWithUTF8String:host], (int)port);
        if (!cfg) return 2;
        return tvApplyProxyConfig(cfg);
    }
}

int tvApplyProxyConfig(id cfg) {
    @autoreleasepool {
        Class dsClass = objc_getClass("WKWebsiteDataStore");
        if (!dsClass) return 1;
        SEL shared = sel_registerName("defaultDataStore");
        id store = ((id(*)(id, SEL))objc_msgSend)((id)dsClass, shared);
        if (!store) return 2;

        NSArray *list = [NSArray arrayWithObject:cfg];
        SEL selSet = sel_registerName("setProxyConfigurations:");
        @try {
            ((void(*)(id, SEL, NSArray *))objc_msgSend)(store, selSet, list);
            return 0;
        } @catch (NSException *ex) {
            NSLog(@"torview: setProxyConfigurations failed: %@", ex);
            return 2;
        }
    }
}
*/
import "C"

import (
	"fmt"
)

// configureProxyCocoa performs the macOS-specific configuration. It must be
// called before webview.New on darwin builds.
func configureProxyEnv(m *torManager) error {
	host := m.SocksAddr()
	// host:port split for the C bridge.
	var h string
	var port int
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			h = host[:i]
			port = atoi(host[i+1:])
			break
		}
	}
	if h == "" || port == 0 || port > 65535 {
		return fmt.Errorf("adresse SOCKS invalide : %s", host)
	}

	rc := C.tvSetSocksProxy(C.CString(h), C.int(port))
	switch rc {
	case 0:
		logLine("[proxy] WKWebView proxyConfigurations: socks5://" + h)
		return nil
	case 1:
		return fmt.Errorf("runtime macOS sans ProxyConfiguration (macOS 14+/iOS 17+ requis) : impossible de garantir le routage Tor")
	case 2:
		return fmt.Errorf("WKWebsiteDataStore a refusé la configuration proxy")
	}
	return nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func platformGuard(*torManager) error { return nil }
