class WebauthnCredential < ApplicationRecord
  # 関係は User の has_many にしか書かれていない
  validates :nickname, uniqueness: { scope: :user_id }
end
