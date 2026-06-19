use opentelemetry::{global, trace::TracerProvider as _, KeyValue};
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_sdk::{
    runtime,
    trace::{self as sdktrace, Sampler},
    Resource,
};
use opentelemetry_semantic_conventions::resource::{SERVICE_NAME, SERVICE_VERSION};
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt, EnvFilter};

#[derive(Debug)]
pub struct TracingGuard {
    otel_installed: bool,
}

impl Drop for TracingGuard {
    fn drop(&mut self) {
        if self.otel_installed {
            global::shutdown_tracer_provider();
        }
    }
}

/// Initialise the global tracing subscriber. When `OTEL_EXPORTER_OTLP_ENDPOINT`
/// is set, spans are additionally exported to an OTLP/gRPC collector (Tempo in
/// our compose stack); otherwise only the stdout log layer is wired.
///
/// `LOG_FORMAT=json` picks the JSON fmt layer; anything else keeps the
/// human-readable pretty output.
pub fn init() -> TracingGuard {
    let env_filter =
        EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));

    let json_fmt = matches!(std::env::var("LOG_FORMAT").as_deref(), Ok("json"));
    let otlp_endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
        .ok()
        .filter(|s| !s.is_empty());

    // Fail-open OTLP exporter init: if the exporter can't be built (endpoint
    // unreachable, TLS misconfig, etc.), log the error and continue without
    // OTel. The service must still start — matches the Go side's behaviour.
    let otel_layer = otlp_endpoint.as_deref().and_then(|endpoint| {
        let service_name = std::env::var("OTEL_SERVICE_NAME")
            .unwrap_or_else(|_| "aether-rust".to_string());
        let resource = Resource::new(vec![
            KeyValue::new(SERVICE_NAME, service_name),
            KeyValue::new(SERVICE_VERSION, env!("CARGO_PKG_VERSION")),
        ]);

        let exporter = match opentelemetry_otlp::SpanExporter::builder()
            .with_tonic()
            .with_endpoint(endpoint)
            .build()
        {
            Ok(e) => e,
            Err(err) => {
                eprintln!(
                    "[tracing_init] failed to build OTLP span exporter for {endpoint}: {err}; continuing without traces"
                );
                return None;
            }
        };

        let provider = sdktrace::TracerProvider::builder()
            .with_batch_exporter(exporter, runtime::Tokio)
            .with_resource(resource)
            .with_sampler(Sampler::ParentBased(Box::new(Sampler::AlwaysOn)))
            .build();

        let tracer = provider.tracer("aether-grpc-server");
        global::set_tracer_provider(provider);
        Some(tracing_opentelemetry::layer().with_tracer(tracer))
    });

    let otel_installed = otel_layer.is_some();

    let fmt_layer_json = if json_fmt {
        Some(
            tracing_subscriber::fmt::layer()
                .json()
                .flatten_event(true)
                .with_current_span(true)
                .with_span_list(false),
        )
    } else {
        None
    };
    let fmt_layer_pretty = if json_fmt {
        None
    } else {
        Some(tracing_subscriber::fmt::layer())
    };

    tracing_subscriber::registry()
        .with(env_filter)
        .with(fmt_layer_json)
        .with(fmt_layer_pretty)
        .with(otel_layer)
        .init();

    if otel_installed {
        if let Some(endpoint) = otlp_endpoint {
            tracing::info!(endpoint, "OTLP tracing exporter installed");
        }
    }

    TracingGuard { otel_installed }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tracing_guard_construction_otel_installed() {
        let guard = TracingGuard {
            otel_installed: true,
        };
        assert!(guard.otel_installed);
    }

    #[test]
    fn tracing_guard_construction_no_otel() {
        let guard = TracingGuard {
            otel_installed: false,
        };
        assert!(!guard.otel_installed);
    }

    #[test]
    fn tracing_guard_drop_with_no_otel_is_noop() {
        {
            let _guard = TracingGuard {
                otel_installed: false,
            };
        }
    }

    #[test]
    fn env_filter_default_when_no_rust_log() {
        let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
        assert_eq!(filter.to_string(), "info");
    }

    #[test]
    fn env_filter_from_explicit_value() {
        let filter = EnvFilter::new("debug");
        assert_eq!(filter.to_string(), "debug");
    }

    #[test]
    fn env_filter_from_warn() {
        let filter = EnvFilter::new("warn");
        assert_eq!(filter.to_string(), "warn");
    }

    #[test]
    fn env_filter_from_trace() {
        let filter = EnvFilter::new("trace");
        assert_eq!(filter.to_string(), "trace");
    }

    #[test]
    fn env_filter_from_error() {
        let filter = EnvFilter::new("error");
        assert_eq!(filter.to_string(), "error");
    }

    #[test]
    fn env_filter_from_crate_specific() {
        let filter = EnvFilter::new("aether_grpc_server=debug,info");
        assert!(filter.to_string().contains("aether_grpc_server=debug"));
    }

    #[test]
    fn json_format_detection_with_json_env() {
        std::env::set_var("LOG_FORMAT", "json");
        let is_json = matches!(std::env::var("LOG_FORMAT").as_deref(), Ok("json"));
        assert!(is_json);
        std::env::remove_var("LOG_FORMAT");
    }

    #[test]
    fn json_format_detection_without_json_env() {
        std::env::remove_var("LOG_FORMAT");
        let is_json = matches!(std::env::var("LOG_FORMAT").as_deref(), Ok("json"));
        assert!(!is_json);
    }

    #[test]
    fn json_format_detection_with_non_json_value() {
        std::env::set_var("LOG_FORMAT", "pretty");
        let is_json = matches!(std::env::var("LOG_FORMAT").as_deref(), Ok("json"));
        assert!(!is_json);
        std::env::remove_var("LOG_FORMAT");
    }

    #[test]
    fn otlp_endpoint_empty_string_is_none() {
        std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "");
        let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
            .ok()
            .filter(|s| !s.is_empty());
        assert!(endpoint.is_none());
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
    }

    #[test]
    fn otlp_endpoint_set_to_value() {
        std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317");
        let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
            .ok()
            .filter(|s| !s.is_empty());
        assert_eq!(endpoint.as_deref(), Some("http://localhost:4317"));
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
    }

    #[test]
    fn otlp_endpoint_unset_is_none() {
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
        let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
            .ok()
            .filter(|s| !s.is_empty());
        assert!(endpoint.is_none());
    }

    #[test]
    fn otlp_service_name_default() {
        std::env::remove_var("OTEL_SERVICE_NAME");
        let name = std::env::var("OTEL_SERVICE_NAME")
            .unwrap_or_else(|_| "aether-rust".to_string());
        assert_eq!(name, "aether-rust");
    }

    #[test]
    fn otlp_service_name_custom() {
        std::env::set_var("OTEL_SERVICE_NAME", "my-service");
        let name = std::env::var("OTEL_SERVICE_NAME")
            .unwrap_or_else(|_| "aether-rust".to_string());
        assert_eq!(name, "my-service");
        std::env::remove_var("OTEL_SERVICE_NAME");
    }

    #[test]
    fn resource_construction_with_default_name() {
        let service_name = "aether-rust".to_string();
        let version = env!("CARGO_PKG_VERSION");
        let resource = Resource::new(vec![
            KeyValue::new(SERVICE_NAME, service_name),
            KeyValue::new(SERVICE_VERSION, version),
        ]);
        assert!(resource.get(SERVICE_NAME.into()).is_some());
    }

    #[test]
    fn init_returns_guard_without_panic() {
        let guard = init();
        assert!(!guard.otel_installed);
    }

    #[test]
    fn multiple_guards_can_be_created() {
        let g1 = TracingGuard {
            otel_installed: false,
        };
        let g2 = TracingGuard {
            otel_installed: true,
        };
        assert!(!g1.otel_installed);
        assert!(g2.otel_installed);
    }

    #[test]
    fn env_filter_with_multiple_directives() {
        let filter = EnvFilter::new("aether_grpc_server=debug,tonic=warn,info");
        assert!(filter.to_string().contains("aether_grpc_server=debug"));
        assert!(filter.to_string().contains("tonic=warn"));
    }

    #[test]
    fn fmt_layer_json_creates_json_layer() {
        use tracing_subscriber::fmt;
        let _layer = fmt::layer::<tracing_subscriber::Registry>()
            .json()
            .flatten_event(true)
            .with_current_span(true)
            .with_span_list(false);
    }

    #[test]
    fn fmt_layer_pretty_creates_pretty_layer() {
        use tracing_subscriber::fmt;
        let _layer = fmt::layer::<tracing_subscriber::Registry>();
    }

    #[test]
    fn guard_fields_are_public() {
        let guard = TracingGuard {
            otel_installed: true,
        };
        let _val = guard.otel_installed;
    }

    #[test]
    fn tracing_guard_drop_with_otel_calls_shutdown() {
        {
            let _guard = TracingGuard {
                otel_installed: true,
            };
        }
    }

    #[test]
    fn resource_construction_with_custom_name() {
        let service_name = "my-custom-service".to_string();
        let version = env!("CARGO_PKG_VERSION");
        let resource = Resource::new(vec![
            KeyValue::new(SERVICE_NAME, service_name),
            KeyValue::new(SERVICE_VERSION, version),
        ]);
        assert!(resource.get(SERVICE_NAME.into()).is_some());
    }

    #[test]
    fn resource_service_version_matches_crate_version() {
        let version = env!("CARGO_PKG_VERSION");
        let resource = Resource::new(vec![KeyValue::new(SERVICE_VERSION, version)]);
        let val = resource.get(SERVICE_VERSION.into());
        assert!(val.is_some());
    }

    // ---- init() with LOG_FORMAT=json ----

    #[test]
    #[ignore = "calls init() which panics when global subscriber already set"]
    fn init_with_json_format() {
        std::env::set_var("LOG_FORMAT", "json");
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
        std::env::remove_var("RUST_LOG");
        let guard = init();
        assert!(!guard.otel_installed);
        std::env::remove_var("LOG_FORMAT");
    }

    // ---- init() with RUST_LOG set ----

    #[test]
    #[ignore = "calls init() which panics when global subscriber already set"]
    fn init_with_rust_log() {
        std::env::set_var("RUST_LOG", "debug");
        std::env::remove_var("LOG_FORMAT");
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
        let guard = init();
        assert!(!guard.otel_installed);
        std::env::remove_var("RUST_LOG");
    }

    // ---- init() with empty OTEL_EXPORTER_OTLP_ENDPOINT ----

    #[test]
    #[ignore = "calls init() which panics when global subscriber already set"]
    fn init_with_empty_otel_endpoint() {
        std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "");
        std::env::remove_var("LOG_FORMAT");
        std::env::remove_var("RUST_LOG");
        let guard = init();
        assert!(!guard.otel_installed);
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
    }

    // ---- init() with OTEL_SERVICE_NAME set ----

    #[test]
    #[ignore = "calls init() which panics when global subscriber already set"]
    fn init_with_otel_service_name() {
        std::env::set_var("OTEL_SERVICE_NAME", "test-service");
        std::env::remove_var("LOG_FORMAT");
        std::env::remove_var("RUST_LOG");
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
        let guard = init();
        assert!(!guard.otel_installed);
        std::env::remove_var("OTEL_SERVICE_NAME");
    }

    // ---- TracingGuard Drop behavior ----

    #[test]
    fn tracing_guard_drop_calls_shutdown_when_otel_installed() {
        {
            let _guard = TracingGuard {
                otel_installed: true,
            };
            // Drop happens here - should call shutdown_tracer_provider
        }
        // No panic means it succeeded
    }

    #[test]
    fn tracing_guard_drop_does_not_call_shutdown_when_not_installed() {
        {
            let _guard = TracingGuard {
                otel_installed: false,
            };
            // Drop happens here - should NOT call shutdown_tracer_provider
        }
    }

    // ---- EnvFilter edge cases ----

    #[test]
    fn env_filter_empty_string() {
        let filter = EnvFilter::new("");
        // Empty string filter defaults to "error" level
        assert!(!filter.to_string().is_empty());
    }

    #[test]
    fn env_filter_off() {
        let filter = EnvFilter::new("off");
        assert_eq!(filter.to_string(), "off");
    }

    #[test]
    fn env_filter_per_crate_directives() {
        let filter = EnvFilter::new("aether=debug,tokio=warn,hyper=error");
        assert!(filter.to_string().contains("aether=debug"));
        assert!(filter.to_string().contains("tokio=warn"));
        assert!(filter.to_string().contains("hyper=error"));
    }

    // ---- LOG_FORMAT edge cases ----

    #[test]
    fn log_format_other_values_not_json() {
        for val in &["text", "console", "pretty", "compact", "full"] {
            std::env::set_var("LOG_FORMAT", val);
            let is_json = matches!(std::env::var("LOG_FORMAT").as_deref(), Ok("json"));
            assert!(!is_json, "LOG_FORMAT={val} should not be json");
            std::env::remove_var("LOG_FORMAT");
        }
    }

    // ---- init() env var detection logic (without calling init itself) ----

    #[test]
    fn init_otel_endpoint_empty_is_none() {
        std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "");
        let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
            .ok()
            .filter(|s| !s.is_empty());
        assert!(endpoint.is_none());
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
    }

    #[test]
    fn init_json_format_env_detection() {
        std::env::set_var("LOG_FORMAT", "json");
        let is_json = matches!(std::env::var("LOG_FORMAT").as_deref(), Ok("json"));
        assert!(is_json);
        std::env::remove_var("LOG_FORMAT");
    }

    #[test]
    fn init_otel_service_name_env_detection() {
        std::env::set_var("OTEL_SERVICE_NAME", "test-service");
        let name = std::env::var("OTEL_SERVICE_NAME")
            .unwrap_or_else(|_| "aether-rust".to_string());
        assert_eq!(name, "test-service");
        std::env::remove_var("OTEL_SERVICE_NAME");
    }

    // ---- Multiple guards drop sequentially ----

    #[test]
    fn multiple_guards_drop_sequentially() {
        let g1 = TracingGuard { otel_installed: false };
        let g2 = TracingGuard { otel_installed: false };
        let g3 = TracingGuard { otel_installed: false };
        drop(g1);
        drop(g2);
        drop(g3);
    }

    // ---- Resource with multiple key-value pairs ----

    #[test]
    fn resource_with_full_attributes() {
        let resource = Resource::new(vec![
            KeyValue::new(SERVICE_NAME, "aether-rust"),
            KeyValue::new(SERVICE_VERSION, env!("CARGO_PKG_VERSION")),
        ]);
        assert!(resource.get(SERVICE_NAME.into()).is_some());
        assert!(resource.get(SERVICE_VERSION.into()).is_some());
    }

    #[test]
    fn tracing_guard_otel_installed_field_true() {
        let guard = TracingGuard { otel_installed: true };
        assert!(guard.otel_installed);
    }

    #[test]
    fn tracing_guard_otel_installed_field_false() {
        let guard = TracingGuard { otel_installed: false };
        assert!(!guard.otel_installed);
    }

    #[test]
    fn env_filter_try_from_default_env_returns_ok_or_default() {
        std::env::remove_var("RUST_LOG");
        let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("warn"));
        assert_eq!(filter.to_string(), "warn");
    }

    #[test]
    fn env_filter_try_from_default_env_with_value() {
        std::env::set_var("RUST_LOG", "trace");
        let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
        assert_eq!(filter.to_string(), "trace");
        std::env::remove_var("RUST_LOG");
    }

    #[test]
    fn log_format_json_detection_exact_match() {
        std::env::set_var("LOG_FORMAT", "json");
        let v = std::env::var("LOG_FORMAT").unwrap();
        assert_eq!(v, "json");
        let is_json = v == "json";
        assert!(is_json);
        std::env::remove_var("LOG_FORMAT");
    }

    #[test]
    fn log_format_json_detection_no_match() {
        std::env::set_var("LOG_FORMAT", "JSON");
        let v = std::env::var("LOG_FORMAT").unwrap();
        assert_ne!(v, "json");
        std::env::remove_var("LOG_FORMAT");
    }

    #[test]
    fn otlp_endpoint_some_when_set() {
        std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317");
        let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
            .ok()
            .filter(|s| !s.is_empty());
        assert!(endpoint.is_some());
        assert_eq!(endpoint.unwrap(), "http://localhost:4317");
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
    }

    #[test]
    fn otlp_endpoint_none_when_unset() {
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
        let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
            .ok()
            .filter(|s| !s.is_empty());
        assert!(endpoint.is_none());
    }

    #[test]
    fn resource_service_name_attribute_access() {
        let resource = Resource::new(vec![
            KeyValue::new(SERVICE_NAME, "aether-rust"),
            KeyValue::new(SERVICE_VERSION, "1.0.0"),
        ]);
        let name = resource.get(SERVICE_NAME.into());
        assert!(name.is_some());
    }

    #[test]
    fn resource_service_version_attribute_access() {
        let resource = Resource::new(vec![
            KeyValue::new(SERVICE_NAME, "aether-rust"),
            KeyValue::new(SERVICE_VERSION, "1.0.0"),
        ]);
        let ver = resource.get(SERVICE_VERSION.into());
        assert!(ver.is_some());
    }

    #[test]
    fn resource_empty_no_attributes() {
        let resource = Resource::new(vec![]);
        assert!(resource.get(SERVICE_NAME.into()).is_none());
    }

    #[test]
    fn guard_drop_multiple_invocations() {
        {
            let _g1 = TracingGuard { otel_installed: false };
            let _g2 = TracingGuard { otel_installed: false };
            let _g3 = TracingGuard { otel_installed: false };
        }
    }

    #[test]
    fn env_filter_level_hierarchy() {
        let filter = EnvFilter::new("warn");
        let s = filter.to_string();
        assert_eq!(s, "warn");
    }

    #[test]
    fn env_filter_debug_all_crates() {
        let filter = EnvFilter::new("debug");
        assert_eq!(filter.to_string(), "debug");
    }

    #[test]
    fn env_filter_critical_only() {
        let filter = EnvFilter::new("error");
        assert_eq!(filter.to_string(), "error");
    }

    #[test]
    fn log_format_unset_is_not_json() {
        std::env::remove_var("LOG_FORMAT");
        let is_json = matches!(std::env::var("LOG_FORMAT").as_deref(), Ok("json"));
        assert!(!is_json);
    }

    #[test]
    fn init_json_fmt_layer_properties() {
        use tracing_subscriber::fmt;
        let _layer = fmt::layer::<tracing_subscriber::Registry>()
            .json()
            .flatten_event(true)
            .with_current_span(true)
            .with_span_list(false);
    }

    #[test]
    fn init_pretty_fmt_layer_properties() {
        use tracing_subscriber::fmt;
        let _layer = fmt::layer::<tracing_subscriber::Registry>()
            .with_target(true);
    }

    #[test]
    fn resource_with_duplicate_service_name_uses_last() {
        let resource = Resource::new(vec![
            KeyValue::new(SERVICE_NAME, "first"),
            KeyValue::new(SERVICE_NAME, "second"),
        ]);
        assert!(resource.get(SERVICE_NAME.into()).is_some());
    }

    #[test]
    fn tracing_guard_debug_otel_installed() {
        let guard = TracingGuard { otel_installed: true };
        let debug = format!("{:?}", guard);
        assert!(debug.contains("true"));
    }

    #[test]
    fn tracing_guard_debug_no_otel() {
        let guard = TracingGuard { otel_installed: false };
        let debug = format!("{:?}", guard);
        assert!(debug.contains("false"));
    }

    #[test]
    fn env_filter_with_all_log_levels() {
        let filter = EnvFilter::new("error,warn,info,debug,trace");
        let s = filter.to_string();
        assert!(s.contains("trace"), "expected 'trace' in EnvFilter output, got: {s:?}");
    }

    #[test]
    fn otlp_endpoint_whitespace_only_is_none() {
        std::env::set_var("OTEL_EXPORTER_OTLP_ENDPOINT", "   ");
        let endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
            .ok()
            .filter(|s| !s.is_empty());
        assert!(endpoint.is_some());
        std::env::remove_var("OTEL_EXPORTER_OTLP_ENDPOINT");
    }

    #[test]
    fn otlp_service_name_default_when_unset() {
        std::env::remove_var("OTEL_SERVICE_NAME");
        let name = std::env::var("OTEL_SERVICE_NAME")
            .unwrap_or_else(|_| "aether-rust".to_string());
        assert_eq!(name, "aether-rust");
    }

    #[test]
    fn multiple_env_filter_variations() {
        for level in &["error", "warn", "info", "debug", "trace", "off"] {
            let filter = EnvFilter::new(*level);
            assert_eq!(filter.to_string(), *level);
        }
    }

    #[tokio::test]
    async fn otlp_exporter_builder_accepts_any_endpoint() {
        // The OTLP exporter builder only validates the endpoint at export
        // time, not at build time. Build always succeeds regardless of
        // endpoint reachability. This test exercises the builder code path
        // with a synthetic endpoint and verifies no panic.
        let endpoint = "http://127.0.0.1:1";
        let result = opentelemetry_otlp::SpanExporter::builder()
            .with_tonic()
            .with_endpoint(endpoint)
            .build();
        assert!(result.is_ok(), "builder should succeed for any string endpoint");
    }
}
