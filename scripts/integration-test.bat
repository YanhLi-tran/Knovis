@echo off
REM 集成测试启动器（Windows）
REM 用法：integration-test.bat [--server URL] [--llm-key KEY] [--skip a,b] [--only a,b]
setlocal
set "SCRIPT_DIR=%~dp0"
if "%SCRIPT_DIR:~-1%"=="\" set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"

where python >nul 2>nul
if %ERRORLEVEL%==0 (
    python "%SCRIPT_DIR%\integration_test.py" %*
    exit /b %ERRORLEVEL%
)

where py >nul 2>nul
if %ERRORLEVEL%==0 (
    py "%SCRIPT_DIR%\integration_test.py" %*
    exit /b %ERRORLEVEL%
)

echo [ERROR] 未找到 Python，请先安装 Python 3.7+ 并加入 PATH
exit /b 2
