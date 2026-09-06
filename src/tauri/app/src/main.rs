#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod protocol;
mod sidecar;

use sidecar::{graceful_quit, send_ui_command, start_owned_sidecar, BridgeState, ServiceStatus};
use std::io::{self, BufRead, Write};
use tauri::{AppHandle, Manager, RunEvent, State, WindowEvent};

const MAX_FIXTURE_LINE_BYTES: usize = 64 * 1024;

#[tauri::command]
fn send_command(
    app: AppHandle,
    command: String,
    state: State<'_, BridgeState>,
) -> Result<(), String> {
    if is_fixture_mode() {
        if !sidecar::valid_ui_command(&command) {
            return Err("AnyPick rejected an invalid fixture command.".into());
        }
        return write_fixture_command(&command);
    }
    if command == "quit" {
        graceful_quit(app);
        return Ok(());
    }
    if state.status().phase == "stopped" {
        return Err("AnyPick core is stopped. Restart the app to try again.".into());
    }
    send_ui_command(&app, &command)
}

#[tauri::command]
fn last_service_line(state: State<'_, BridgeState>) -> Option<String> {
    state.last_service_line()
}

#[tauri::command]
fn last_supervisor_line(state: State<'_, BridgeState>) -> Option<String> {
    state.last_service_line()
}

#[tauri::command]
fn service_status(state: State<'_, BridgeState>) -> ServiceStatus {
    state.status()
}

fn write_fixture_command(command: &str) -> Result<(), String> {
    let stdout = io::stdout();
    let mut output = stdout.lock();
    writeln!(output, "{command}").map_err(|_| "Fixture bridge is unavailable.".to_string())?;
    output
        .flush()
        .map_err(|_| "Fixture bridge is unavailable.".to_string())
}

fn valid_fixture_line(line: &str) -> bool {
    line.len() <= MAX_FIXTURE_LINE_BYTES
        && ["snapshot\t", "result\t", "logs\t", "status\t"]
            .iter()
            .any(|prefix| line.starts_with(prefix))
}

fn is_protocol_mode(smoke: bool, probe: bool) -> bool {
    smoke || probe
}

fn is_fixture_mode() -> bool {
    std::env::var_os("ANYPICK_TRAY_DEMO").is_some()
}

fn command_for_probe(kind: &str) -> Option<&'static str> {
    match kind {
        "refresh" => Some("refresh"),
        "logs" => Some("logs\te30="),
        "mutate" => Some("mutate\te30="),
        "invoke" => Some("invoke\te30="),
        "model-roles" => Some("model-roles\te30="),
        "navigate" => Some("navigate\taccounts"),
        "quit" => Some("quit"),
        _ => None,
    }
}

fn run_protocol_mode(smoke: bool, probe: bool) {
    // This explicit CI seam returns before Tauri/GTK/WebKit initialization.
    // Production never reads its parent stdin or writes commands to stdout.
    let stdin = io::stdin();
    for line in stdin.lock().lines().map_while(Result::ok) {
        if probe {
            if let Some(kind) = line.strip_prefix("probe\t") {
                if let Some(command) = command_for_probe(kind) {
                    let _ = write_fixture_command(command);
                    if command == "quit" {
                        return;
                    }
                }
                continue;
            }
        }
        if smoke && valid_fixture_line(&line) {
            let _ = write_fixture_command("refresh");
            return;
        }
    }
}

fn read_fixture(app: tauri::AppHandle) {
    let stdin = io::stdin();
    for line in stdin.lock().lines().map_while(Result::ok) {
        if valid_fixture_line(&line) {
            sidecar::deliver_fixture_line(&app, line);
        }
    }
    app.exit(0);
}

fn main() {
    let smoke = std::env::var_os("ANYPICK_TRAY_SMOKE").is_some();
    let probe = std::env::var_os("ANYPICK_TRAY_PROBE").is_some();
    if is_protocol_mode(smoke, probe) {
        run_protocol_mode(smoke, probe);
        return;
    }

    let app = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(BridgeState::new())
        .invoke_handler(tauri::generate_handler![
            send_command,
            last_service_line,
            last_supervisor_line,
            service_status
        ])
        // App-first, matching the macOS app: the main window is the product.
        // There is no tray icon — on Linux and Windows closing the window quits
        // the app, and the Go sidecar is owned here, not by a tray utility.
        .setup(|app| {
            let handle = app.handle().clone();
            if is_fixture_mode() {
                sidecar::mark_fixture_ready(&handle);
                std::thread::spawn(move || read_fixture(handle));
            } else {
                start_owned_sidecar(handle);
            }
            Ok(())
        })
        // Closing the window quits. Do not hide-and-linger: without a tray icon
        // there would be no way to reach the app again.
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { .. } = event {
                // graceful_quit stops the owned Go sidecar before exiting.
                graceful_quit(window.app_handle().clone());
            }
        })
        .build(tauri::generate_context!())
        .expect("failed to build the AnyPick desktop app");
    app.run(|app, event| {
        if let RunEvent::ExitRequested { api, .. } = event {
            if !app.state::<BridgeState>().is_shutting_down() {
                api.prevent_exit();
                graceful_quit(app.clone());
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fixture_accepts_only_bounded_service_output() {
        assert!(valid_fixture_line("snapshot\te30="));
        assert!(valid_fixture_line("result\te30="));
        assert!(!valid_fixture_line("invoke\te30="));
        assert!(!valid_fixture_line(&"x".repeat(MAX_FIXTURE_LINE_BYTES + 1)));
    }

    #[test]
    fn protocol_modes_bypass_interactive_runtime() {
        assert!(is_protocol_mode(true, false));
        assert!(is_protocol_mode(false, true));
        assert!(!is_protocol_mode(false, false));
    }

    #[test]
    fn probe_commands_are_bounded_to_the_supported_bridge_surface() {
        assert_eq!(command_for_probe("refresh"), Some("refresh"));
        assert_eq!(command_for_probe("model-roles"), Some("model-roles\te30="));
        assert_eq!(command_for_probe("navigate"), Some("navigate\taccounts"));
        assert_eq!(command_for_probe("quit"), Some("quit"));
        assert_eq!(command_for_probe("unknown"), None);
    }
}
