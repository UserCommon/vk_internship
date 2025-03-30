box.cfg{
    listen = 3301,
    memtx_memory = 128 * 1024 * 1024
}

-- Удаляем старое пространство (если есть)
if box.space.polls ~= nil then
    box.space.polls:drop()
end

-- Создаем новое пространство
box.schema.create_space('polls', {
    format = {
        {name = 'id', type = 'string'},
        {name = 'question', type = 'string'},
        {name = 'options', type = 'map'},
        {name = 'votes', type = 'map'},
        {name = 'creator', type = 'string'},
        {name = 'active', type = 'boolean'},
        {name = 'created_at', type = 'number'},
        --{name = 'voted_users', type = 'map'}
    }
})

-- Создаем индекс
box.space.polls:create_index('primary', {parts = {'id'}})

-- Создаем пользователя (если не существует)
if not box.schema.user.exists('voting_bot') then
    box.schema.user.create('voting_bot', {password = '123321'})
end

-- Даем права (без избыточных проверок)
box.schema.user.grant('voting_bot', 'read,write,execute', 'space', 'polls')
