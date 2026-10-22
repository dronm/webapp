-- Apply constants/sql/001_constants.up.sql first using your migration runner.
-- Use stable codes in application code; generated IDs may differ by environment.
-- Existing administrator values are preserved when this seed is rerun.
INSERT INTO public.webapp_constants (code, name, description, value_type, value)
VALUES
	('allow_order_creation', 'Разрешить создание заказов',
	 'Разрешает пользователям создавать новые заказы.', 'bool', 'true'::jsonb),
	('order_timeout_seconds', 'Время ожидания заказа',
	 'Максимальное время ожидания в секундах: от 1 до 3600.', 'int64', '300'::jsonb),
	('default_price_type', 'Вид цены по умолчанию',
	 'Вид цены, используемый при создании нового заказа.', 'json',
	 '{"id":1,"name":"Розничная"}'::jsonb)
ON CONFLICT (code) DO NOTHING;
