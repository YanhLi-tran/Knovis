@echo off
REM local-agent 一键启动脚本（Windows）
REM 用法：
REM   start-local-agent.bat                         # 交互式输入 token
REM   start-local-agent.bat --token YOUR_JWT_TOKEN  # 直接传 token
REM   start-local-agent.bat --token YOUR_JWT_TOKEN --server ws://1.2.3.4:8001
REM
REM 环境变量优先级：命令行参数 > 环境变量 > 交互输入

setlocal ENABLEDELAYEDEXPANSION

REM 定位脚本所在目录（支持从 dist 子目录双击启动）
set "SCRIPT_DIR=%~dp0"
if "%SCRIPT_DIR:~-1%"=="\" set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"

REM 默认参数
set "SERVER=ws://127.0.0.1:8001"
set "TOKEN=%AGENT_TOKEN%"

REM 解析命令行参数
:parse
if "%~1"=="" goto start
if /I "%~1"=="--token" ( set "TOKEN=%~2" & shift & shift & goto parse )
if /I "%~1"=="--server" ( set "SERVER=%~2" & shift & shift & goto parse )
if /I "%~1"=="-h" goto usage
if /I "%~1"=="--help" goto usage
echo [ERROR] 未知参数: %~1
goto usage

:start
if "%TOKEN%"=="" (
    echo.
    echo ============================================
    echo   local-agent 启动器
    echo ============================================
    echo.
    set /p "TOKEN=请输入 JWT access token: "
    if "!TOKEN!"=="" (
        echo [ERROR] token 不能为空
        exit /b 1
    )
)

REM 选择二进制：优先同目录 local-agent.exe，回退到 dist\local-agent-windows-amd64.exe
set "BIN="
if exist "%SCRIPT_DIR%\local-agent.exe" set "BIN=%SCRIPT_DIR%\local-agent.exe"
if not defined BIN if exist "%SCRIPT_DIR%\dist\local-agent-windows-amd64.exe" set "BIN=%SCRIPT_DIR%\dist\local-agent-windows-amd64.exe"
if not defined BIN if exist "%SCRIPT_DIR%\local-agent\local-agent.exe" set "BIN=%SCRIPT_DIR%\local-agent\local-agent.exe"

if not defined BIN (
    echo [ERROR] 未找到 local-agent.exe
    echo.
    echo 请先编译：
    echo   powershell -ExecutionPolicy Bypass -File "%SCRIPT_DIR%\scripts\build-local-agent.ps1" -Targets windows-amd64
    echo 或：
    echo   cd local-agent ^&^& go build -o local-agent.exe .
    exit /b 1
)

echo.
echo [INFO] 二进制: %BIN%
echo [INFO] 服务器: %SERVER%
echo [INFO] 启动中... (Ctrl+C 退出)
echo.

"%BIN%" -server "%SERVER%" -token "%TOKEN%"
REM 异常退出时停留窗口便于看错误
if %ERRORLEVEL% NEQ 0 (
    echo.
    echo [ERROR] 进程退出 code=%ERRORLEVEL%
    pause
)
exit /b %ERRORLEVEL%

:usage
echo.
echo local-agent 启动器（Windows）
echo.
echo 用法：
echo   %~nx0 [--token TOKEN] [--server URL]
echo.
echo 参数：
echo   --token TOKEN   JWT access token（不传则从 AGENT_TOKEN 环境变量读，再没有则交互输入）
echo   --server URL    服务器 WebSocket 地址（默认 ws://127.0.0.1:8001）
echo.
echo 环境变量：
echo   AGENT_TOKEN     JWT access token（与 --token 等价）
echo.
exit /b 0
