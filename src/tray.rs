#[cfg(not(windows))]
pub fn start(_app: crate::App, _port: u16) -> Option<std::thread::JoinHandle<()>> {
    None
}

#[cfg(not(windows))]
pub fn language() -> String {
    "en".into()
}

#[cfg(windows)]
pub use windows::{language, start};

#[cfg(windows)]
mod windows {
    use std::{
        cell::RefCell,
        collections::HashSet,
        mem::size_of,
        os::windows::ffi::OsStrExt,
        path::Path,
        ptr::{null, null_mut},
        thread,
    };

    use windows_sys::Win32::{
        Foundation::{HWND, LPARAM, LRESULT, POINT, WPARAM},
        Globalization::GetUserDefaultLocaleName,
        System::{LibraryLoader::GetModuleHandleW, RemoteDesktop::ProcessIdToSessionId},
        UI::{
            Shell::{
                NIF_ICON, NIF_INFO, NIF_MESSAGE, NIF_TIP, NIM_ADD, NIM_DELETE, NIM_MODIFY,
                NOTIFYICONDATAW, Shell_NotifyIconW, ShellExecuteW,
            },
            WindowsAndMessaging::*,
        },
    };

    use crate::{
        App, VERSION,
        operations::{self, translate},
    };

    const ICON: u32 = WM_APP + 1;
    const OPEN_LOG: usize = 1;
    const OPEN_DATA: usize = 2;
    const INSTALL: usize = 3;
    const STOP: usize = 4;

    struct Context {
        app: App,
        port: u16,
        announced: String,
    }

    thread_local! {
        static CONTEXT: RefCell<Option<Context>> = const { RefCell::new(None) };
    }

    pub fn language() -> String {
        let mut buf = [0; 85];
        // Win32 writes at most the supplied capacity, including its terminator.
        let length = unsafe { GetUserDefaultLocaleName(buf.as_mut_ptr(), buf.len() as i32) };
        if length > 0 {
            String::from_utf16_lossy(&buf[..length as usize - 1])
        } else {
            "en".into()
        }
    }

    pub fn start(app: App, port: u16) -> Option<thread::JoinHandle<()>> {
        let mut session = 0;
        // Session zero is reserved for services and has no interactive desktop.
        if app.config.no_tray
            || unsafe { ProcessIdToSessionId(std::process::id(), &mut session) } == 0
            || session == 0
        {
            return None;
        }
        Some(thread::spawn(move || window_loop(app, port)))
    }

    fn wide(text: &str) -> Vec<u16> {
        text.encode_utf16().chain([0]).collect()
    }

    fn fill(buffer: &mut [u16], text: &str) {
        buffer.fill(0);
        let max = buffer.len().saturating_sub(1);
        for (slot, value) in buffer[..max].iter_mut().zip(text.encode_utf16()) {
            *slot = value;
        }
    }

    fn window_loop(app: App, port: u16) {
        CONTEXT.with(|context| {
            context.replace(Some(Context {
                app,
                port,
                announced: String::new(),
            }))
        });
        let name = wide("EditorCollaborationRustTray");
        // All HWND and tray resources are used exclusively by this message-loop thread.
        unsafe {
            let instance = GetModuleHandleW(null());
            let class = WNDCLASSW {
                hInstance: instance,
                lpfnWndProc: Some(window_proc),
                lpszClassName: name.as_ptr(),
                ..Default::default()
            };
            if RegisterClassW(&class) == 0 {
                return;
            }
            let window = CreateWindowExW(
                0,
                name.as_ptr(),
                name.as_ptr(),
                0,
                0,
                0,
                0,
                0,
                null_mut(),
                null_mut(),
                instance,
                null(),
            );
            if window.is_null() {
                return;
            }
            let mut icon = icon_data(window);
            icon.uFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP;
            icon.uCallbackMessage = ICON;
            icon.hIcon = LoadIconW(null_mut(), IDI_APPLICATION);
            fill(&mut icon.szTip, "Editor Collaboration");
            if Shell_NotifyIconW(NIM_ADD, &icon) == 0 || SetTimer(window, 1, 2000, None) == 0 {
                DestroyWindow(window);
                return;
            }
            let mut message = MSG::default();
            while GetMessageW(&mut message, null_mut(), 0, 0) > 0 {
                TranslateMessage(&message);
                DispatchMessageW(&message);
            }
        }
    }

    fn icon_data(window: HWND) -> NOTIFYICONDATAW {
        NOTIFYICONDATAW {
            cbSize: size_of::<NOTIFYICONDATAW>() as u32,
            hWnd: window,
            uID: 1,
            ..Default::default()
        }
    }

    fn status(app: &App) -> String {
        let state = app.state.lock().unwrap();
        let clients = state.clients.len();
        let rooms = state
            .clients
            .values()
            .map(|peer| &peer.room)
            .collect::<HashSet<_>>()
            .len();
        match (clients, rooms) {
            (0, _) => translate("tray.noClients", &[]),
            (1, _) => translate("tray.oneClient", &[]),
            (clients, 1) => translate("tray.manyClients", &[&clients.to_string()]),
            (clients, rooms) => translate(
                "tray.manyRooms",
                &[&clients.to_string(), &rooms.to_string()],
            ),
        }
    }

    fn refresh(window: HWND, context: &mut Context) -> bool {
        if *context.app.stop.borrow() {
            return false;
        }
        let mut icon = icon_data(window);
        icon.uFlags = NIF_TIP;
        fill(
            &mut icon.szTip,
            &format!("Editor Collaboration - {}", status(&context.app)),
        );
        if let Some(release) = context.app.update.lock().unwrap().as_ref() {
            if release.available && release.version != context.announced {
                context.announced.clone_from(&release.version);
                icon.uFlags |= NIF_INFO;
                fill(&mut icon.szInfoTitle, "Editor Collaboration");
                fill(
                    &mut icon.szInfo,
                    &translate("tray.updateBalloon", &[&release.version]),
                );
            }
        }
        // The struct remains valid for the duration of the synchronous Win32 call.
        unsafe {
            Shell_NotifyIconW(NIM_MODIFY, &icon);
        }
        true
    }

    fn open(window: HWND, path: &Path) {
        let path: Vec<u16> = path.as_os_str().encode_wide().chain([0]).collect();
        unsafe {
            ShellExecuteW(
                window,
                wide("open").as_ptr(),
                path.as_ptr(),
                null(),
                null(),
                SW_SHOWNORMAL,
            );
        }
    }

    fn menu(window: HWND, app: &App, port: u16) {
        let status = status(app);
        let latest = app
            .update
            .lock()
            .unwrap()
            .as_ref()
            .filter(|release| release.available)
            .map(|release| release.version.clone());
        // The popup and its strings are local to the owning window thread.
        let selected = unsafe {
            let menu = CreatePopupMenu();
            if menu.is_null() {
                return;
            }
            let item = |flags, id, text: &str| {
                AppendMenuW(menu, flags, id, wide(text).as_ptr());
            };
            item(MF_GRAYED, 0, &format!("Editor Collaboration {VERSION}"));
            item(MF_GRAYED, 0, &status);
            item(MF_GRAYED, 0, &translate("tray.port", &[&port.to_string()]));
            AppendMenuW(menu, MF_SEPARATOR, 0, null());
            if app.config.log.is_some() {
                item(MF_STRING, OPEN_LOG, &translate("tray.openLogs", &[]));
            }
            item(MF_STRING, OPEN_DATA, &translate("tray.openData", &[]));
            if let Some(version) = latest {
                item(
                    MF_STRING,
                    INSTALL,
                    &translate("tray.installUpdate", &[&version]),
                );
            }
            item(MF_STRING, STOP, &translate("tray.stop", &[]));
            let mut point = POINT::default();
            GetCursorPos(&mut point);
            SetForegroundWindow(window);
            let selected = TrackPopupMenu(
                menu,
                TPM_RETURNCMD | TPM_RIGHTBUTTON,
                point.x,
                point.y,
                0,
                window,
                null(),
            );
            PostMessageW(window, WM_NULL, 0, 0);
            DestroyMenu(menu);
            selected as usize
        };
        match selected {
            OPEN_LOG => {
                if let Some(path) = &app.config.log {
                    open(window, path);
                }
            }
            OPEN_DATA => open(window, &app.config.data),
            INSTALL => operations::install_offered(app.clone()),
            STOP => {
                app.stop.send_replace(true);
            }
            _ => {}
        }
    }

    unsafe extern "system" fn window_proc(
        window: HWND,
        message: u32,
        wparam: WPARAM,
        lparam: LPARAM,
    ) -> LRESULT {
        match message {
            WM_TIMER => {
                let alive = CONTEXT.with(|context| {
                    context
                        .try_borrow_mut()
                        .ok()
                        .and_then(|mut context| {
                            context.as_mut().map(|context| refresh(window, context))
                        })
                        .unwrap_or(true)
                });
                if !alive {
                    unsafe {
                        DestroyWindow(window);
                    }
                }
                0
            }
            ICON if matches!(lparam as u32, WM_LBUTTONUP | WM_RBUTTONUP) => {
                let data = CONTEXT
                    .with(|context| context.borrow().as_ref().map(|c| (c.app.clone(), c.port)));
                if let Some((app, port)) = data {
                    menu(window, &app, port);
                }
                0
            }
            WM_DESTROY => {
                unsafe {
                    KillTimer(window, 1);
                    Shell_NotifyIconW(NIM_DELETE, &icon_data(window));
                    PostQuitMessage(0);
                }
                0
            }
            _ => unsafe { DefWindowProcW(window, message, wparam, lparam) },
        }
    }
}
